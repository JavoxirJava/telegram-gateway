package messages

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Page struct {
	Items      []Message
	NextCursor string
}

type cursorPayload struct {
	Version           int   `json:"v"`
	SentAtUnixMicro   int64 `json:"t"`
	TelegramMessageID int64 `json:"m"`
}

func EncodeCursor(cursor Cursor) (string, error) {
	if cursor.SentAt.IsZero() || cursor.TelegramMessageID == 0 {
		return "", errors.New("cursor requires sent time and Telegram message id")
	}
	payload := cursorPayload{
		Version:           1,
		SentAtUnixMicro:   cursor.SentAt.UTC().UnixMicro(),
		TelegramMessageID: cursor.TelegramMessageID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal message cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func DecodeCursor(value string) (Cursor, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Cursor{}, errors.New("cursor is empty")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Cursor{}, errors.New("invalid cursor encoding")
	}
	var payload cursorPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return Cursor{}, errors.New("invalid cursor payload")
	}
	if payload.Version != 1 || payload.SentAtUnixMicro <= 0 || payload.TelegramMessageID == 0 {
		return Cursor{}, errors.New("invalid cursor values")
	}
	return Cursor{
		SentAt:            time.UnixMicro(payload.SentAtUnixMicro).UTC(),
		TelegramMessageID: payload.TelegramMessageID,
	}, nil
}

func (r *Repository) ListActivePageByChat(ctx context.Context, accountID, chatID string, cursor *Cursor, limit int) (Page, error) {
	limit = normalizeLimit(limit)
	queryLimit := limit + 1

	var cursorTime *time.Time
	var cursorMessageID *int64
	if cursor != nil {
		t := cursor.SentAt.UTC()
		cursorTime = &t
		cursorMessageID = &cursor.TelegramMessageID
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id::text, account_id::text, chat_id::text, telegram_message_id,
		       sender_telegram_id, sender_chat_id, message_type, content,
		       content_entities, reply_to_message_id, forward_info, raw_metadata,
		       sent_at, edited_at
		FROM active_messages
		WHERE account_id = $1::uuid
		  AND chat_id = $2::uuid
		  AND (
		      $3::timestamptz IS NULL
		      OR (sent_at, telegram_message_id) < ($3::timestamptz, $4::bigint)
		  )
		ORDER BY sent_at DESC, telegram_message_id DESC
		LIMIT $5`,
		accountID, chatID, cursorTime, cursorMessageID, queryLimit,
	)
	if err != nil {
		return Page{}, fmt.Errorf("list active message page: %w", err)
	}
	defer rows.Close()

	items, err := scanMessages(rows)
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: items}
	if len(items) <= limit {
		return page, nil
	}

	page.Items = items[:limit]
	last := page.Items[len(page.Items)-1]
	next, err := EncodeCursor(Cursor{SentAt: last.SentAt, TelegramMessageID: last.TelegramMessageID})
	if err != nil {
		return Page{}, err
	}
	page.NextCursor = next
	return page, nil
}
