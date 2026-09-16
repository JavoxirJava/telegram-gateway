package worker

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/JavoxirJava/telegram-gateway/internal/chats"
	"github.com/JavoxirJava/telegram-gateway/internal/syncjob"
)

func (p *Processor) handleAccountBootstrap(ctx context.Context, envelope syncjob.Envelope, payload syncjob.AccountBootstrapPayload) error {
	lease, err := p.acquireSyncLease(ctx, envelope.AccountID, nil, "chats")
	if err != nil {
		return err
	}

	session, err := p.sessions.Get(ctx, envelope.AccountID)
	if err != nil {
		return p.failSync(ctx, lease, err)
	}

	if strings.TrimSpace(payload.Cursor) == "" {
		if err := p.beforeTelegram(ctx, envelope.AccountID, "profile", p.policies.History); err != nil {
			return p.failSync(ctx, lease, err)
		}
		profile, err := session.Profile(ctx)
		if err != nil {
			return p.failSync(ctx, lease, p.telegramError(ctx, envelope.AccountID, err))
		}
		if _, err := p.accounts.Activate(ctx, envelope.AccountID, profile.TelegramUserID, profile.DisplayName, profile.Username); err != nil {
			return p.failSync(ctx, lease, err)
		}
	}

	if err := p.beforeTelegram(ctx, envelope.AccountID, "list_chats", p.policies.History); err != nil {
		return p.failSync(ctx, lease, err)
	}
	page, err := session.ListChats(ctx, payload.Cursor, 100)
	if err != nil {
		return p.failSync(ctx, lease, p.telegramError(ctx, envelope.AccountID, err))
	}

	for _, item := range page.Items {
		dbChatID, err := p.chats.Upsert(ctx, chats.Chat{
			AccountID:      envelope.AccountID,
			TelegramChatID: item.TelegramChatID,
			ChatType:       strings.TrimSpace(item.Type),
			Title:          optionalString(item.Title),
			Username:       optionalString(item.Username),
			Metadata:       item.Metadata,
			LastMessageID:  item.LastMessageID,
			LastMessageAt:  item.LastMessageAt,
		})
		if err != nil {
			return p.failSync(ctx, lease, err)
		}

		state, err := p.syncStates.Ensure(ctx, envelope.AccountID, &dbChatID, "history")
		if err != nil {
			return p.failSync(ctx, lease, err)
		}
		before, stopAt := int64(0), int64(0)
		if v, ok := state.Cursor["before_message_id"].(float64); ok {
			before = int64(v)
		}
		if v, ok := state.Cursor["before_message_id"].(json.Number); ok {
			before, _ = v.Int64()
		}
		if before == 0 && state.NewestMessageID != nil {
			stopAt = *state.NewestMessageID
		}
		if err := p.publisher.EnqueueChatHistory(ctx, envelope.AccountID, syncjob.ChatHistoryPayload{
			ChatID:          dbChatID,
			BeforeMessageID: before, StopAfterMessageID: stopAt,
			TelegramChatID:    item.TelegramChatID,
			RequestedPageSize: 100,
		}); err != nil {
			return p.failSync(ctx, lease, err)
		}

		if shouldSyncMembers(item.Type) {
			if err := p.publisher.EnqueueChatMembers(ctx, envelope.AccountID, syncjob.ChatMembersPayload{
				ChatID:         dbChatID,
				TelegramChatID: item.TelegramChatID,
				Limit:          200,
			}); err != nil {
				return p.failSync(ctx, lease, err)
			}
		}
	}

	if strings.TrimSpace(payload.Cursor) == "" {
		if err := p.publisher.EnqueueContactsSync(ctx, envelope.AccountID); err != nil {
			return p.failSync(ctx, lease, err)
		}
	}
	if next := strings.TrimSpace(page.NextCursor); next != "" {
		if err := p.publisher.EnqueueAccountBootstrapPage(ctx, envelope.AccountID, next); err != nil {
			return p.failSync(ctx, lease, err)
		}
	}

	if err := p.syncStates.Progress(ctx, lease, map[string]any{"next_cursor": page.NextCursor}, nil, nil); err != nil {
		return p.failSync(ctx, lease, err)
	}
	if err := p.syncStates.Complete(ctx, lease); err != nil {
		return err
	}
	return p.writeAudit(ctx, envelope.AccountID, "TELEGRAM_CHATS_SYNCED", "telegram_account", envelope.AccountID, map[string]any{
		"count":       len(page.Items),
		"has_more":    page.NextCursor != "",
		"force":       payload.Force,
		"used_cursor": payload.Cursor != "",
	})
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func shouldSyncMembers(chatType string) bool {
	switch strings.ToLower(strings.TrimSpace(chatType)) {
	case "group", "supergroup", "channel":
		return true
	default:
		return false
	}
}
