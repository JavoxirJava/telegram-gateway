package contacts

import (
	"context"
	"fmt"
	"strings"
)

// MarkMissingDeleted finishes a full Telegram contacts snapshot. Contacts not
// present in seenTelegramUserIDs are retained but hidden from normal reads.
func (r *Repository) MarkMissingDeleted(ctx context.Context, accountID string, seenTelegramUserIDs []int64) (int64, error) {
	result, err := r.pool.Exec(ctx, `
		UPDATE telegram_contacts
		SET deleted = TRUE,
		    deleted_at = COALESCE(deleted_at, NOW()),
		    updated_at = NOW()
		WHERE account_id = $1::uuid
		  AND deleted = FALSE
		  AND NOT (telegram_user_id = ANY($2::bigint[]))`,
		strings.TrimSpace(accountID), seenTelegramUserIDs)
	if err != nil {
		return 0, fmt.Errorf("soft-delete contacts missing from snapshot: %w", err)
	}
	return result.RowsAffected(), nil
}
