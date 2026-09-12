package worker

import (
	"context"
	"errors"
	"strings"

	"github.com/JavoxirJava/telegram-gateway/internal/messages"
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
	page, err := session.GetChatHistory(ctx, payload.TelegramChatID, payload.BeforeMessageID, payload.RequestedPageSize)
	if err != nil {
		return p.failSync(ctx, lease, p.telegramError(ctx, envelope.AccountID, err))
	}

	if err := page.Validate(payload.BeforeMessageID, payload.RequestedPageSize); err != nil {
		return p.failSync(ctx, lease, Permanent(err))
	}
	items := page.Items
	var oldestMessageID *int64
	var newestMessageID *int64
	for _, item := range items {
		messageID := item.TelegramMessageID
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

		dbMessageID, err := p.messages.Upsert(ctx, messages.Message{
			AccountID:         envelope.AccountID,
			ChatID:            chatID,
			TelegramMessageID: item.TelegramMessageID,
			SenderTelegramID:  item.SenderTelegramID,
			SenderChatID:      item.SenderChatID,
			MessageType:       item.Type,
			Content:           item.Content,
			ContentEntities:   item.Entities,
			ReplyToMessageID:  item.ReplyToMessageID,
			ForwardInfo:       item.ForwardInfo,
			RawMetadata:       item.Metadata,
			SentAt:            item.SentAt,
			EditedAt:          item.EditedAt,
		})
		if err != nil {
			return p.failSync(ctx, lease, err)
		}

		for _, attachment := range item.Media {
			var telegramFileID *int64
			if attachment.TelegramFileID != 0 {
				value := attachment.TelegramFileID
				telegramFileID = &value
			}
			mediaID, err := p.mediaRepo.RegisterPending(
				ctx,
				dbMessageID,
				attachment.Type,
				telegramFileID,
				optionalString(attachment.UniqueFileKey),
				optionalString(attachment.MIMEType),
				optionalString(attachment.FileName),
				attachment.FileSize,
			)
			if err != nil {
				return p.failSync(ctx, lease, err)
			}
			if telegramFileID != nil {
				if err := p.publisher.EnqueueMediaDownload(ctx, envelope.AccountID, syncjob.MediaDownloadPayload{
					MediaID:        mediaID,
					MessageID:      dbMessageID,
					TelegramFileID: *telegramFileID,
				}); err != nil {
					return p.failSync(ctx, lease, err)
				}
			}
		}
	}

	nextBefore := page.NextBeforeMessageID
	if !page.Exhausted {
		if payload.BeforeMessageID > 0 && nextBefore >= payload.BeforeMessageID {
			return p.failSync(ctx, lease, Permanent(errors.New("Telegram history pagination did not advance")))
		}
		if err := p.publisher.EnqueueChatHistory(ctx, envelope.AccountID, syncjob.ChatHistoryPayload{
			ChatID:            chatID,
			TelegramChatID:    payload.TelegramChatID,
			BeforeMessageID:   nextBefore,
			RequestedPageSize: payload.RequestedPageSize,
		}); err != nil {
			return p.failSync(ctx, lease, err)
		}
	}

	if err := p.syncStates.Progress(ctx, lease, map[string]any{
		"before_message_id": nextBefore,
		"page_count":        len(items),
		"source_count":      page.SourceCount,
		"exhausted":         page.Exhausted,
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
