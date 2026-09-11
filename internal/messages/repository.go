package messages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultPageSize = 30
	maxPageSize     = 100
)

type Message struct {
	ID                string         `json:"id"`
	AccountID         string         `json:"account_id"`
	ChatID            string         `json:"chat_id"`
	TelegramMessageID int64          `json:"telegram_message_id"`
	SenderTelegramID  *int64         `json:"sender_telegram_id,omitempty"`
	SenderChatID      *int64         `json:"sender_chat_id,omitempty"`
	MessageType       string         `json:"message_type"`
	Content           *string        `json:"content,omitempty"`
	ContentEntities   []any          `json:"content_entities,omitempty"`
	ReplyToMessageID  *int64         `json:"reply_to_message_id,omitempty"`
	ForwardInfo       map[string]any `json:"forward_info,omitempty"`
	RawMetadata       map[string]any `json:"raw_metadata,omitempty"`
	SentAt            time.Time      `json:"sent_at"`
	EditedAt          *time.Time     `json:"edited_at,omitempty"`
}

type Cursor struct {
	SentAt            time.Time
	TelegramMessageID int64
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Upsert(ctx context.Context, message Message) (string, error) {
	if strings.TrimSpace(message.AccountID) == "" || strings.TrimSpace(message.ChatID) == "" {
		return "", errors.New("account id and chat id are required")
	}
	if message.TelegramMessageID == 0 {
		return "", errors.New("telegram message id is required")
	}
	if strings.TrimSpace(message.MessageType) == "" {
		return "", errors.New("message type is required")
	}
	if message.SentAt.IsZero() {
		return "", errors.New("sent_at is required")
	}

	entitiesJSON, err := marshalArrayJSON(message.ContentEntities)
	if err != nil {
		return "", fmt.Errorf("marshal content entities: %w", err)
	}
	forwardJSON, err := marshalNullableJSON(message.ForwardInfo)
	if err != nil {
		return "", fmt.Errorf("marshal forward info: %w", err)
	}
	metadataJSON, err := marshalObjectJSON(message.RawMetadata)
	if err != nil {
		return "", fmt.Errorf("marshal raw metadata: %w", err)
	}

	var id string
	err = r.pool.QueryRow(ctx, `
		INSERT INTO messages (
			account_id, chat_id, telegram_message_id, sender_telegram_id, sender_chat_id,
			message_type, content, content_entities, reply_to_message_id, forward_info,
			raw_metadata, sent_at, edited_at
		) VALUES (
			$1::uuid, $2::uuid, $3, $4, $5,
			$6, $7, $8::jsonb, $9, $10::jsonb,
			$11::jsonb, $12, $13
		)
		ON CONFLICT (account_id, chat_id, telegram_message_id)
		DO UPDATE SET
			sender_telegram_id = EXCLUDED.sender_telegram_id,
			sender_chat_id = EXCLUDED.sender_chat_id,
			message_type = EXCLUDED.message_type,
			content = EXCLUDED.content,
			content_entities = EXCLUDED.content_entities,
			reply_to_message_id = EXCLUDED.reply_to_message_id,
			forward_info = EXCLUDED.forward_info,
			raw_metadata = EXCLUDED.raw_metadata,
			sent_at = EXCLUDED.sent_at,
			edited_at = EXCLUDED.edited_at,
			updated_at = NOW()
		RETURNING id::text`,
		message.AccountID,
		message.ChatID,
		message.TelegramMessageID,
		message.SenderTelegramID,
		message.SenderChatID,
		message.MessageType,
		message.Content,
		string(entitiesJSON),
		message.ReplyToMessageID,
		nullableJSONString(forwardJSON),
		string(metadataJSON),
		message.SentAt.UTC(),
		message.EditedAt,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("upsert message: %w", err)
	}
	return id, nil
}

func (r *Repository) MarkDeleted(ctx context.Context, accountID, chatID string, telegramMessageID int64, deletedAt time.Time) error {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(chatID) == "" {
		return errors.New("account id and chat id are required")
	}
	if telegramMessageID == 0 {
		return errors.New("telegram message id is required")
	}
	if deletedAt.IsZero() {
		deletedAt = time.Now().UTC()
	}

	_, err := r.pool.Exec(ctx, `
		UPDATE messages
		SET deleted = TRUE,
			deleted_at = COALESCE(deleted_at, $4),
			updated_at = NOW()
		WHERE account_id = $1::uuid
		  AND chat_id = $2::uuid
		  AND telegram_message_id = $3`,
		accountID, chatID, telegramMessageID, deletedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("soft-delete message: %w", err)
	}
	return nil
}

func (r *Repository) ListActiveByChat(ctx context.Context, accountID, chatID string, cursor *Cursor, limit int) ([]Message, error) {
	limit = normalizeLimit(limit)

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
		accountID, chatID, cursorTime, cursorMessageID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list active messages: %w", err)
	}
	defer rows.Close()

	return scanMessages(rows)
}

func (r *Repository) SearchActive(ctx context.Context, accountID, query string, limit int) ([]Message, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search query is required")
	}
	limit = normalizeLimit(limit)

	rows, err := r.pool.Query(ctx, `
		SELECT id::text, account_id::text, chat_id::text, telegram_message_id,
		       sender_telegram_id, sender_chat_id, message_type, content,
		       content_entities, reply_to_message_id, forward_info, raw_metadata,
		       sent_at, edited_at
		FROM active_messages
		WHERE account_id = $1::uuid
		  AND content ILIKE ('%' || $2 || '%') ESCAPE '\'
		ORDER BY sent_at DESC, telegram_message_id DESC
		LIMIT $3`,
		accountID, escapeLike(query), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search active messages: %w", err)
	}
	defer rows.Close()

	return scanMessages(rows)
}

type rowsScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanMessages(rows rowsScanner) ([]Message, error) {
	result := make([]Message, 0)
	for rows.Next() {
		var message Message
		var entitiesJSON []byte
		var forwardJSON []byte
		var metadataJSON []byte

		if err := rows.Scan(
			&message.ID,
			&message.AccountID,
			&message.ChatID,
			&message.TelegramMessageID,
			&message.SenderTelegramID,
			&message.SenderChatID,
			&message.MessageType,
			&message.Content,
			&entitiesJSON,
			&message.ReplyToMessageID,
			&forwardJSON,
			&metadataJSON,
			&message.SentAt,
			&message.EditedAt,
		); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}

		if len(entitiesJSON) > 0 {
			_ = json.Unmarshal(entitiesJSON, &message.ContentEntities)
		}
		if len(forwardJSON) > 0 {
			_ = json.Unmarshal(forwardJSON, &message.ForwardInfo)
		}
		if len(metadataJSON) > 0 {
			_ = json.Unmarshal(metadataJSON, &message.RawMetadata)
		}
		result = append(result, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return result, nil
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return defaultPageSize
	}
	if limit > maxPageSize {
		return maxPageSize
	}
	return limit
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}

func marshalArrayJSON(value []any) ([]byte, error) {
	if value == nil {
		value = []any{}
	}
	return json.Marshal(value)
}

func marshalObjectJSON(value map[string]any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	return json.Marshal(value)
}

func marshalNullableJSON(value map[string]any) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	return json.Marshal(value)
}

func nullableJSONString(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}
