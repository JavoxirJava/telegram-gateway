package media

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const staleDownloadAfter = 15 * time.Minute

// ClaimDownloadRecoverable claims pending/failed work and can recover a
// download abandoned by a crashed worker after the stale threshold.
func (r *Repository) ClaimDownloadRecoverable(ctx context.Context, mediaID string, maxAttempts int) (bool, error) {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	result, err := r.pool.Exec(ctx, `
		UPDATE message_media
		SET download_status = 'downloading',
		    attempt_count = attempt_count + 1,
		    last_attempt_at = NOW(),
		    last_error = NULL,
		    updated_at = NOW()
		WHERE id = $1::uuid
		  AND attempt_count < $2
		  AND (
		      download_status IN ('pending', 'failed')
		      OR (
		          download_status = 'downloading'
		          AND last_attempt_at IS NOT NULL
		          AND last_attempt_at <= NOW() - ($3 * interval '1 millisecond')
		      )
		  )`, strings.TrimSpace(mediaID), maxAttempts, staleDownloadAfter.Milliseconds())
	if err != nil {
		return false, fmt.Errorf("claim recoverable media download: %w", err)
	}
	return result.RowsAffected() == 1, nil
}
