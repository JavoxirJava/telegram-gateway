package worker

import (
	"context"
	"errors"
	"strings"

	"github.com/JavoxirJava/telegram-gateway/internal/syncjob"
)

func (p *Processor) handleChatHistory(ctx context.Context, envelope syncjob.Envelope, payload syncjob.ChatHistoryPayload) error {
	chatID := strings.TrimSpace(payload.ChatID)
	if chatID == "" || payload.TelegramChatID == 0 {
		return Permanent(errors.New("chat id and Telegram chat id are required"))
	}
	if payload.RequestedPageSize <= 0 {
		payload.RequestedPageSize = 100
	}
	if payload.RequestedPageSize > 100 {
		payload.RequestedPageSize = 100
	}

	lease, err := p.acquireSyncLease(ctx, envelope.AccountID, &chatID, "history")
	if err != nil {
		return err
	}
	if err := p.beforeTelegram(ctx, envelope.AccountID, "chat_history", p.policies.History); err != nil {
		return p.failSync(ctx, lease, err)
	}

	session, err := p.sessions.Get(ctx, envelope.AccountID)
	if err != nil {
		return p.failSync(ctx, lease, err)
	}
	items, err := session.GetChatHistory(ctx, payload.TelegramChatID, payload.BeforeMessageID, payload.RequestedPageSize)
	if err != nil {
		return p.failSync(ctx, lease, p.telegramError(ctx, envelope.AccountID, err))
	}

	reachedKnown := false
	var oldestMessageID *int64
	var newestMessageID *int64
	for _, item := range items {
		messageID := item.TelegramMessageID
		if payload.StopAfterMessageID > 0 && messageID <= payload.StopAfterMessageID {
			reachedKnown = true
			break
		}
		if messageID == 0 || item.SentAt.IsZero() {
			return p.failSync(ctx, lease, Permanent(errors.New("Telegram history returned an invalid message")))
		}
		if oldestMessageID == nil || messageID < *oldestMessageID {
			value := messageID
			oldestMessageID = &value
		}
		if newestMessageID == nil || messageID > *newestMessageID {
			value := messageID
			newestMessageID = &value
		}

		if err := p.StoreMessage(ctx, envelope.AccountID, chatID, item); err != nil {
			return p.failSync(ctx, lease, err)
		}
	}

	nextBefore := int64(0)
	// TDLib may return a short page while older messages still exist. Continue
	// until an empty page instead of treating a short page as end-of-history.
	if !reachedKnown && len(items) > 0 && oldestMessageID != nil {
		nextBefore = *oldestMessageID
		if nextBefore == payload.BeforeMessageID {
			return p.failSync(ctx, lease, Permanent(errors.New("Telegram history pagination did not advance")))
		}
		if err := p.publisher.EnqueueChatHistory(ctx, envelope.AccountID, syncjob.ChatHistoryPayload{
			ChatID:             chatID,
			StopAfterMessageID: payload.StopAfterMessageID,
			TelegramChatID:     payload.TelegramChatID,
			BeforeMessageID:    nextBefore,
			RequestedPageSize:  payload.RequestedPageSize,
		}); err != nil {
			return p.failSync(ctx, lease, err)
		}
	}

	if err := p.syncStates.Progress(ctx, lease, map[string]any{
		"before_message_id": nextBefore,
		"page_count":        len(items),
	}, oldestMessageID, newestMessageID); err != nil {
		return p.failSync(ctx, lease, err)
	}
	if err := p.syncStates.Complete(ctx, lease); err != nil {
		return err
	}
	return p.writeAudit(ctx, envelope.AccountID, "TELEGRAM_CHAT_HISTORY_SYNCED", "chat", chatID, map[string]any{
		"count":               len(items),
		"before_message_id":   payload.BeforeMessageID,
		"next_before_message": nextBefore,
		"has_more":            nextBefore != 0,
	})
}
