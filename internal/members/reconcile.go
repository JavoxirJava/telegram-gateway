package members

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MarkNotSeenSince completes a paginated member snapshot. Every member upsert
// refreshes last_seen_at; once the final page is processed, older rows are
// retained but soft-deleted.
func (r *Repository) MarkNotSeenSince(ctx context.Context, accountID, chatID string, snapshotStartedAt time.Time) (int64, error) {
	result, err := r.pool.Exec(ctx, `
		UPDATE chat_members
		SET deleted = TRUE,
		    deleted_at = COALESCE(deleted_at, NOW()),
		    updated_at = NOW()
		WHERE account_id = $1::uuid
		  AND chat_id = $2::uuid
		  AND deleted = FALSE
		  AND last_seen_at < $3`,
		strings.TrimSpace(accountID), strings.TrimSpace(chatID), snapshotStartedAt.UTC())
	if err != nil {
		return 0, fmt.Errorf("soft-delete members not seen in snapshot: %w", err)
	}
	return result.RowsAffected(), nil
}
