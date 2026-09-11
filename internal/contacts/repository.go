package contacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultLimit = 50
	maxLimit     = 100
)

type Contact struct {
	ID             string         `json:"id"`
	TelegramUserID int64          `json:"telegram_user_id"`
	FirstName      *string        `json:"first_name,omitempty"`
	LastName       *string        `json:"last_name,omitempty"`
	Username       *string        `json:"username,omitempty"`
	IsMutual       bool           `json:"is_mutual"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Upsert(ctx context.Context, accountID string, contact Contact, phoneHash []byte) (string, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || contact.TelegramUserID <= 0 {
		return "", errors.New("account id and positive Telegram user id are required")
	}
	if contact.Metadata == nil {
		contact.Metadata = map[string]any{}
	}
	metadataJSON, err := json.Marshal(contact.Metadata)
	if err != nil {
		return "", fmt.Errorf("marshal contact metadata: %w", err)
	}

	var id string
	err = r.pool.QueryRow(ctx, `
		INSERT INTO telegram_contacts (
			account_id, telegram_user_id, first_name, last_name, username,
			phone_hash, is_mutual, raw_metadata
		) VALUES (
			$1::uuid, $2, $3, $4, $5, $6, $7, $8::jsonb
		)
		ON CONFLICT (account_id, telegram_user_id)
		DO UPDATE SET
			first_name = EXCLUDED.first_name,
			last_name = EXCLUDED.last_name,
			username = EXCLUDED.username,
			phone_hash = COALESCE(EXCLUDED.phone_hash, telegram_contacts.phone_hash),
			is_mutual = EXCLUDED.is_mutual,
			raw_metadata = EXCLUDED.raw_metadata,
			deleted = FALSE,
			deleted_at = NULL,
			updated_at = NOW()
		RETURNING id::text`,
		accountID,
		contact.TelegramUserID,
		trimPtr(contact.FirstName),
		trimPtr(contact.LastName),
		trimPtr(contact.Username),
		nullableBytes(phoneHash),
		contact.IsMutual,
		string(metadataJSON),
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("upsert contact: %w", err)
	}
	return id, nil
}

func (r *Repository) MarkDeleted(ctx context.Context, accountID string, telegramUserID int64) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE telegram_contacts
		SET deleted = TRUE,
		    deleted_at = COALESCE(deleted_at, NOW()),
		    updated_at = NOW()
		WHERE account_id = $1::uuid
		  AND telegram_user_id = $2`, strings.TrimSpace(accountID), telegramUserID)
	if err != nil {
		return fmt.Errorf("soft-delete contact: %w", err)
	}
	return nil
}

func (r *Repository) ListActive(ctx context.Context, accountID string, limit int) ([]Contact, error) {
	return r.query(ctx, accountID, "", normalizeLimit(limit))
}

func (r *Repository) SearchActive(ctx context.Context, accountID, query string, limit int) ([]Contact, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search query is required")
	}
	return r.query(ctx, accountID, query, normalizeLimit(limit))
}

func (r *Repository) query(ctx context.Context, accountID, query string, limit int) ([]Contact, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, telegram_user_id, first_name, last_name, username, is_mutual, raw_metadata
		FROM active_contacts
		WHERE account_id = $1::uuid
		  AND (
		      $2 = ''
		      OR COALESCE(first_name, '') ILIKE ('%' || $2 || '%') ESCAPE '\'
		      OR COALESCE(last_name, '') ILIKE ('%' || $2 || '%') ESCAPE '\'
		      OR COALESCE(username, '') ILIKE ('%' || $2 || '%') ESCAPE '\'
		  )
		ORDER BY COALESCE(first_name, ''), COALESCE(last_name, ''), telegram_user_id
		LIMIT $3`, strings.TrimSpace(accountID), escapeLike(query), limit)
	if err != nil {
		return nil, fmt.Errorf("query contacts: %w", err)
	}
	defer rows.Close()

	items := make([]Contact, 0)
	for rows.Next() {
		var item Contact
		var metadataJSON []byte
		if err := rows.Scan(&item.ID, &item.TelegramUserID, &item.FirstName, &item.LastName, &item.Username, &item.IsMutual, &metadataJSON); err != nil {
			return nil, fmt.Errorf("scan contact: %w", err)
		}
		if len(metadataJSON) > 0 {
			_ = json.Unmarshal(metadataJSON, &item.Metadata)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate contacts: %w", err)
	}
	return items, nil
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

func trimPtr(value *string) any {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}
