package worker

import (
	"context"
	"encoding/json"
	"errors"

	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/chats"
	"github.com/JavoxirJava/telegram-gateway/internal/messages"
	"github.com/JavoxirJava/telegram-gateway/internal/syncjob"
	"github.com/JavoxirJava/telegram-gateway/internal/tdlib"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (p *Processor) StoreMessage(ctx context.Context, accountID, chatID string, item telegram.Message) error {
	id, err := p.messages.Upsert(ctx, messages.Message{AccountID: accountID, ChatID: chatID, TelegramMessageID: item.TelegramMessageID, SenderTelegramID: item.SenderTelegramID, SenderChatID: item.SenderChatID, MessageType: item.Type, Content: item.Content, ContentEntities: item.Entities, ReplyToMessageID: item.ReplyToMessageID, ForwardInfo: item.ForwardInfo, RawMetadata: item.Metadata, SentAt: item.SentAt, EditedAt: item.EditedAt})
	if err != nil {
		return err
	}
	if err := p.registerMedia(ctx, accountID, id, item.Media); err != nil {
		return err
	}
	return p.messages.ApplyTombstones(ctx, accountID, chatID)
}
func (p *Processor) registerMedia(ctx context.Context, accountID, messageID string, items []telegram.Media) error {
	for _, attachment := range items {
		fileID := attachment.TelegramFileID
		id, err := p.mediaRepo.RegisterPending(ctx, messageID, attachment.Type, &fileID, optionalString(attachment.UniqueFileKey), optionalString(attachment.MIMEType), optionalString(attachment.FileName), attachment.FileSize)
		if err != nil {
			return err
		}
		if fileID != 0 {
			if err := p.publisher.EnqueueMediaDownload(ctx, accountID, syncjob.MediaDownloadPayload{MediaID: id, MessageID: messageID, TelegramFileID: fileID}); err != nil {
				return err
			}
		}
	}
	return nil
}
func (p *Processor) ProcessUpdate(ctx context.Context, pool *pgxpool.Pool, accountID string, e tdlib.Event) error {
	if e.Chat != nil && e.Chat.Type == "secret" {
		return nil
	}
	if e.Kind == "updateDeleteMessages" && e.Permanent {
		for _, id := range e.MessageIDs {
			if _, err := pool.Exec(ctx, `INSERT INTO message_tombstones(account_id,telegram_chat_id,telegram_message_id) VALUES($1::uuid,$2,$3) ON CONFLICT DO NOTHING`, accountID, e.TelegramChatID, id); err != nil {
				return err
			}
		}
	}
	chatID := ""
	if e.Chat != nil {
		var err error
		c := e.Chat
		chatID, err = p.chats.Upsert(ctx, chats.Chat{AccountID: accountID, TelegramChatID: c.TelegramChatID, ChatType: c.Type, Title: optionalString(c.Title), Username: optionalString(c.Username), Metadata: c.Metadata, LastMessageID: c.LastMessageID, LastMessageAt: c.LastMessageAt})
		if err != nil {
			return err
		}
	} else {
		_ = pool.QueryRow(ctx, `SELECT id::text FROM chats WHERE account_id=$1::uuid AND telegram_chat_id=$2`, accountID, e.TelegramChatID).Scan(&chatID)
	}
	if chatID == "" {
		if e.Kind == "updateDeleteMessages" {
			return nil
		}
		return errors.New("Telegram update chat has not arrived")
	}
	if e.Message != nil {
		if err := p.StoreMessage(ctx, accountID, chatID, *e.Message); err != nil {
			return err
		}
	}
	switch e.Kind {
	case "updateMessageContent":
		encoded, err := json.Marshal(e.Entities)
		if err != nil {
			return err
		}
		if string(encoded) == "null" {
			encoded = []byte("[]")
		}
		var messageID string
		err = pool.QueryRow(ctx, `UPDATE messages SET content=$4,content_entities=$5::jsonb,edited_at=NOW(),updated_at=NOW() WHERE account_id=$1::uuid AND chat_id=$2::uuid AND telegram_message_id=$3 RETURNING id::text`, accountID, chatID, e.MessageID, e.Content, string(encoded)).Scan(&messageID)
		if err == nil {
			return p.registerMedia(ctx, accountID, messageID, e.Media)
		}
	case "updateMessageEdited":
		_, err := pool.Exec(ctx, `UPDATE messages SET edited_at=$4,updated_at=NOW() WHERE account_id=$1::uuid AND chat_id=$2::uuid AND telegram_message_id=$3`, accountID, chatID, e.MessageID, e.EditedAt)
		if err != nil {
			return err
		}
	case "updateDeleteMessages":
		if e.Permanent {
			if err := p.messages.ApplyTombstones(ctx, accountID, chatID); err != nil {
				return err
			}
		}
	}
	_, err := pool.Exec(ctx, `UPDATE telegram_accounts SET last_update_at=NOW() WHERE id=$1::uuid`, accountID)
	return err
}

// RunUpdates drains the durable journal in arrival order. Failed writes remain
// in the journal for retry; data is acknowledged only after it is persisted.
func (p *Processor) RunUpdates(ctx context.Context, pool *pgxpool.Pool) error {
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		rows, err := pool.Query(ctx, `SELECT id,account_id::text,payload FROM telegram_updates ORDER BY id LIMIT 100`)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
				continue
			}
		}
		type update struct {
			id      int64
			account string
			body    []byte
		}
		var batch []update
		for rows.Next() {
			var u update
			if err := rows.Scan(&u.id, &u.account, &u.body); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, u)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, u := range batch {
			var e tdlib.Event
			if err := json.Unmarshal(u.body, &e); err != nil {
				return err
			}
			if err := p.ProcessUpdate(ctx, pool, u.account, e); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				select {
				case <-ctx.Done():
					return nil
				case <-tick.C:
				}
				break
			}
			if _, err := pool.Exec(ctx, `DELETE FROM telegram_updates WHERE id=$1`, u.id); err != nil {
				break
			}
		}
		if len(batch) < 100 {
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
			}
		}
	}
}
