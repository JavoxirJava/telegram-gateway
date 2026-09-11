package chats

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

type Chat struct {
	ID              string         `json:"id"`
	AccountID       string         `json:"account_id"`
	TelegramChatID  int64          `json:"telegram_chat_id"`
	ChatType        string         `json:"chat_type"`
	Title           *string        `json:"title,omitempty"`
	Username        *string        `json:"username,omitempty"`
	PhotoObjectKey  *string        `json:"photo_object_key,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	LastMessageID   *int64         `json:"last_message_id,omitempty"`
	LastMessageAt   *time.Time     `json:"last_message_at,omitempty"`
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Upsert(ctx context.Context, chat Chat) (string, error) {
	if strings.TrimSpace(chat.AccountID) == "" {
		return "", errors.New("account id is required")
	}
	if chat.TelegramChatID == 0 {
		return "", errors.New("telegram chat id is required")
	}
	if strings.TrimSpace(chat.ChatType) == "" {
		return "", errors.New("chat type is required")
	}

	if chat.Metadata == nil {
		chat.Metadata = map[string]any{}
	}
	metadataJSON, err := json.Marshal(chat.Metadata)
	if err != nil {
		return "", fmt.Errorf("marshal chat metadata: %w", err)
	}

	var id string
	err = r.pool.QueryRow(ctx, `
		INSERT INTO chats (
			account_id, telegram_chat_id, chat_type, title, username,
			photo_object_key, metadata, last_message_id, last_message_at
		) VALUES (
			$1::uuid, $2, $3, $4, $5,
			$6, $7::jsonb, $8, $9
		)
		ON CONFLICT (account_id, telegram_chat_id)
		DO UPDATE SET
			chat_type = EXCLUDED.chat_type,
			title = EXCLUDED.title,
			username = EXCLUDED.username,
			photo_object_key = COALESCE(EXCLUDED.photo_object_key, chats.photo_object_key),
			metadata = EXCLUDED.metadata,
			last_message_id = EXCLUDED.last_message_id,
			last_message_at = EXCLUDED.last_message_at,
			updated_at = NOW()
		RETURNING id::text`,
		chat.AccountID,
		chat.TelegramChatID,
		chat.ChatType,
		chat.Title,
		chat.Username,
		chat.PhotoObjectKey,
		string(metadataJSON),
		chat.LastMessageID,
		chat.LastMessageAt,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("upsert chat: %w", err)
	}
	return id, nil
}

func (r *Repository) MarkDeleted(ctx context.Context, accountID string, telegramChatID int64, deletedAt time.Time) error {
	if strings.TrimSpace(accountID) == "" {
		return errors.New("account id is required")
	}
	if telegramChatID == 0 {
		return errors.New("telegram chat id is required")
	}
	if deletedAt.IsZero() {
		deletedAt = time.Now().UTC()
	}

	_, err := r.pool.Exec(ctx, `
		UPDATE chats
		SET deleted = TRUE,
			deleted_at = COALESCE(deleted_at, $3),
			updated_at = NOW()
		WHERE account_id = $1::uuid
		  AND telegram_chat_id = $2`,
		accountID, telegramChatID, deletedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("soft-delete chat: %w", err)
	}
	return nil
}

func (r *Repository) ListActive(ctx context.Context, accountID string, limit int) ([]Chat, error) {
	limit = normalizeLimit(limit)
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, account_id::text, telegram_chat_id, chat_type,
		       title, username, photo_object_key, metadata,
		       last_message_id, last_message_at
		FROM active_chats
		WHERE account_id = $1::uuid
		ORDER BY last_message_at DESC NULLS LAST, telegram_chat_id DESC
		LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("list active chats: %w", err)
	}
	defer rows.Close()

	result := make([]Chat, 0)
	for rows.Next() {
		var chat Chat
		var metadataJSON []byte
		if err := rows.Scan(
			&chat.ID,
			&chat.AccountID,
			&chat.TelegramChatID,
			&chat.ChatType,
			&chat.Title,
			&chat.Username,
			&chat.PhotoObjectKey,
			&metadataJSON,
			&chat.LastMessageID,
			&chat.LastMessageAt,
		); err != nil {
			return nil, fmt.Errorf("scan chat: %w", err)
		}
		if len(metadataJSON) > 0 {
			_ = json.Unmarshal(metadataJSON, &chat.Metadata)
		}
		result = append(result, chat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chats: %w", err)
	}
	return result, nil
}

func (r *Repository) SearchActive(ctx context.Context, accountID, query string, limit int) ([]Chat, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search query is required")
	}
	limit = normalizeLimit(limit)

	rows, err := r.pool.Query(ctx, `
		SELECT id::text, account_id::text, telegram_chat_id, chat_type,
		       title, username, photo_object_key, metadata,
		       last_message_id, last_message_at
		FROM active_chats
		WHERE account_id = $1::uuid
		  AND (COALESCE(title, '') ILIKE ('%' || $2 || '%') ESCAPE '\'
		       OR COALESCE(username, '') ILIKE ('%' || $2 || '%') ESCAPE '\')
		ORDER BY last_message_at DESC NULLS LAST, telegram_chat_id DESC
		LIMIT $3`, accountID, escapeLike(query), limit)
	if err != nil {
		return nil, fmt.Errorf("search active chats: %w", err)
	}
	defer rows.Close()

	result := make([]Chat, 0)
	for rows.Next() {
		var chat Chat
		var metadataJSON []byte
		if err := rows.Scan(
			&chat.ID,
			&chat.AccountID,
			&chat.TelegramChatID,
			&chat.ChatType,
			&chat.Title,
			&chat.Username,
			&chat.PhotoObjectKey,
			&metadataJSON,
			&chat.LastMessageID,
			&chat.LastMessageAt,
		); err != nil {
			return nil, fmt.Errorf("scan chat search result: %w", err)
		}
		if len(metadataJSON) > 0 {
			_ = json.Unmarshal(metadataJSON, &chat.Metadata)
		}
		result = append(result, chat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chat search results: %w", err)
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
