package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/members"
	"github.com/JavoxirJava/telegram-gateway/internal/syncjob"
)

type memberPagePublisher interface {
	EnqueueChatMembersPage(ctx context.Context, accountID string, payload syncjob.ChatMembersPayload) error
}

func (p *Processor) handleChatMembers(ctx context.Context, envelope syncjob.Envelope, payload syncjob.ChatMembersPayload) error {
	chatID := strings.TrimSpace(payload.ChatID)
	if chatID == "" || payload.TelegramChatID == 0 {
		return Permanent(errors.New("chat id and Telegram chat id are required"))
	}
	if payload.Limit <= 0 {
		payload.Limit = 200
	}
	if payload.Limit > 200 {
		payload.Limit = 200
	}

	state, err := p.syncStates.Ensure(ctx, envelope.AccountID, &chatID, "members")
	if err != nil {
		return err
	}
	snapshotStartedAt := time.Now().UTC().Truncate(time.Microsecond)
	if strings.TrimSpace(payload.Cursor) != "" {
		value, ok := state.Cursor["snapshot_started_at"].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return Permanent(errors.New("member snapshot cursor is missing start time"))
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return Permanent(fmt.Errorf("parse member snapshot start time: %w", err))
		}
		snapshotStartedAt = parsed.UTC()
	}

	lease, ok, err := p.syncStates.Acquire(ctx, state.ID, p.workerID, syncLeaseTTL)
	if err != nil {
		return err
	}
	if !ok {
		return RetryAfter(2*time.Second, errors.New("member sync is owned by another worker"))
	}

	if err := p.beforeTelegram(ctx, envelope.AccountID, "list_members", p.policies.Search); err != nil {
		return p.failSync(ctx, lease, err)
	}
	session, err := p.sessions.Get(ctx, envelope.AccountID)
	if err != nil {
		return p.failSync(ctx, lease, err)
	}
	page, err := session.ListMembers(ctx, payload.TelegramChatID, payload.Cursor, payload.Limit)
	if err != nil {
		return p.failSync(ctx, lease, p.telegramError(ctx, envelope.AccountID, err))
	}

	for _, item := range page.Items {
		if _, err := p.members.Upsert(ctx, envelope.AccountID, chatID, members.Member{
			PeerType:       item.PeerType,
			TelegramPeerID: item.TelegramPeerID,
			FirstName:      optionalString(item.FirstName),
			LastName:       optionalString(item.LastName),
			Username:       optionalString(item.Username),
			Role:           optionalString(item.Role),
			Metadata:       item.Metadata,
		}); err != nil {
			return p.failSync(ctx, lease, err)
		}
	}

	nextCursor := strings.TrimSpace(page.NextCursor)
	cursorState := map[string]any{
		"snapshot_started_at": snapshotStartedAt.Format(time.RFC3339Nano),
		"next_cursor":         nextCursor,
	}
	if err := p.syncStates.Progress(ctx, lease, cursorState, nil, nil); err != nil {
		return p.failSync(ctx, lease, err)
	}

	if nextCursor != "" {
		pagedPublisher, ok := p.publisher.(memberPagePublisher)
		if !ok {
			return p.failSync(ctx, lease, Permanent(errors.New("publisher does not support paginated member sync")))
		}
		if err := pagedPublisher.EnqueueChatMembersPage(ctx, envelope.AccountID, syncjob.ChatMembersPayload{
			ChatID:         chatID,
			TelegramChatID: payload.TelegramChatID,
			Cursor:         nextCursor,
			Limit:          payload.Limit,
		}); err != nil {
			return p.failSync(ctx, lease, err)
		}
	} else {
		if _, err := p.members.MarkNotSeenSince(ctx, envelope.AccountID, chatID, snapshotStartedAt); err != nil {
			return p.failSync(ctx, lease, err)
		}
	}

	if err := p.syncStates.Complete(ctx, lease); err != nil {
		return err
	}
	return p.writeAudit(ctx, envelope.AccountID, "TELEGRAM_CHAT_MEMBERS_SYNCED", "chat", chatID, map[string]any{
		"count":    len(page.Items),
		"has_more": nextCursor != "",
	})
}
