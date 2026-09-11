package members

import (
	"context"
	"fmt"
	"strings"
)

type PeerKey struct {
	PeerType       string
	TelegramPeerID int64
}

// MarkMissingDeleted finishes a full member snapshot for one chat. Missing
// members are soft-deleted and remain preserved in storage.
func (r *Repository) MarkMissingDeleted(ctx context.Context, accountID, chatID string, seen []PeerKey) (int64, error) {
	userIDs := make([]int64, 0)
	chatIDs := make([]int64, 0)
	for _, key := range seen {
		switch strings.ToLower(strings.TrimSpace(key.PeerType)) {
		case "user":
			userIDs = append(userIDs, key.TelegramPeerID)
		case "chat":
			chatIDs = append(chatIDs, key.TelegramPeerID)
		}
	}

	result, err := r.pool.Exec(ctx, `
		UPDATE chat_members
		SET deleted = TRUE,
		    deleted_at = COALESCE(deleted_at, NOW()),
		    updated_at = NOW()
		WHERE account_id = $1::uuid
		  AND chat_id = $2::uuid
		  AND deleted = FALSE
		  AND (
		      (peer_type = 'user' AND NOT (telegram_peer_id = ANY($3::bigint[])))
		      OR
		      (peer_type = 'chat' AND NOT (telegram_peer_id = ANY($4::bigint[])))
		  )`,
		strings.TrimSpace(accountID), strings.TrimSpace(chatID), userIDs, chatIDs)
	if err != nil {
		return 0, fmt.Errorf("soft-delete members missing from snapshot: %w", err)
	}
	return result.RowsAffected(), nil
}
