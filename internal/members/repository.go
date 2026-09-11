package members

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
	defaultLimit = 50
	maxLimit     = 100
)

type Member struct {
	ID             string         `json:"id"`
	PeerType       string         `json:"peer_type"`
	TelegramPeerID int64          `json:"telegram_peer_id"`
	FirstName      *string        `json:"first_name,omitempty"`
	LastName       *string        `json:"last_name,omitempty"`
	Username       *string        `json:"username,omitempty"`
	Role           *string        `json:"role,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	LastSeenAt     time.Time      `json:"last_seen_at"`
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Upsert(ctx context.Context, accountID, chatID string, member Member) (string, error) {
	accountID = strings.TrimSpace(accountID)
	chatID = strings.TrimSpace(chatID)
	member.PeerType = strings.ToLower(strings.TrimSpace(member.PeerType))
	if member.PeerType == "" {
		member.PeerType = "user"
	}
	if accountID == "" || chatID == "" || member.TelegramPeerID == 0 {
		return "", errors.New("account id, chat id and Telegram peer id are required")
	}
	if member.PeerType != "user" && member.PeerType != "chat" {
		return "", errors.New("peer type must be user or chat")
	}
	if member.Metadata == nil {
		member.Metadata = map[string]any{}
	}
	metadataJSON, err := json.Marshal(member.Metadata)
	if err != nil {
		return "", fmt.Errorf("marshal member metadata: %w", err)
	}

	var id string
	err = r.pool.QueryRow(ctx, `
		INSERT INTO chat_members (
			account_id, chat_id, peer_type, telegram_peer_id,
			first_name, last_name, username, role, raw_metadata, last_seen_at
		) VALUES (
			$1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9::jsonb, NOW()
		)
		ON CONFLICT (account_id, chat_id, peer_type, telegram_peer_id)
		DO UPDATE SET
			first_name = EXCLUDED.first_name,
			last_name = EXCLUDED.last_name,
			username = EXCLUDED.username,
			role = EXCLUDED.role,
			raw_metadata = EXCLUDED.raw_metadata,
			deleted = FALSE,
			deleted_at = NULL,
			last_seen_at = NOW(),
			updated_at = NOW()
		RETURNING id::text`,
		accountID,
		chatID,
		member.PeerType,
		member.TelegramPeerID,
		trimPtr(member.FirstName),
		trimPtr(member.LastName),
		trimPtr(member.Username),
		trimPtr(member.Role),
		string(metadataJSON),
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("upsert chat member: %w", err)
	}
	return id, nil
}

func (r *Repository) MarkDeleted(ctx context.Context, accountID, chatID, peerType string, telegramPeerID int64) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE chat_members
		SET deleted = TRUE,
		    deleted_at = COALESCE(deleted_at, NOW()),
		    updated_at = NOW()
		WHERE account_id = $1::uuid
		  AND chat_id = $2::uuid
		  AND peer_type = $3
		  AND telegram_peer_id = $4`,
		strings.TrimSpace(accountID), strings.TrimSpace(chatID), strings.ToLower(strings.TrimSpace(peerType)), telegramPeerID)
	if err != nil {
		return fmt.Errorf("soft-delete chat member: %w", err)
	}
	return nil
}

func (r *Repository) ListActive(ctx context.Context, accountID, chatID string, limit int) ([]Member, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, peer_type, telegram_peer_id, first_name, last_name,
		       username, role, raw_metadata, last_seen_at
		FROM active_chat_members
		WHERE account_id = $1::uuid
		  AND chat_id = $2::uuid
		ORDER BY COALESCE(first_name, ''), COALESCE(last_name, ''), telegram_peer_id
		LIMIT $3`, strings.TrimSpace(accountID), strings.TrimSpace(chatID), normalizeLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list active chat members: %w", err)
	}
	defer rows.Close()

	items := make([]Member, 0)
	for rows.Next() {
		var item Member
		var metadataJSON []byte
		if err := rows.Scan(
			&item.ID,
			&item.PeerType,
			&item.TelegramPeerID,
			&item.FirstName,
			&item.LastName,
			&item.Username,
			&item.Role,
			&metadataJSON,
			&item.LastSeenAt,
		); err != nil {
			return nil, fmt.Errorf("scan chat member: %w", err)
		}
		if len(metadataJSON) > 0 {
			_ = json.Unmarshal(metadataJSON, &item.Metadata)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chat members: %w", err)
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
