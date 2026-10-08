package oauth

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CleanupRegistrations expires unused registrations. Consented clients and
// registrations referenced by an authorization flow are never evicted.
func CleanupRegistrations(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := pruneRegistrations(ctx, tx, 1000); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func pruneRegistrations(ctx context.Context, tx pgx.Tx, keep int) error {
	if err := lockRegistrations(ctx, tx); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `WITH unused AS (
		SELECT c.id,c.created_at,row_number() OVER (ORDER BY c.created_at DESC,c.id DESC) AS position
		FROM oauth_clients c WHERE c.activated_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM oauth_requests r WHERE r.client_id=c.id)
		AND NOT EXISTS (SELECT 1 FROM oauth_codes r WHERE r.client_id=c.id)
		AND NOT EXISTS (SELECT 1 FROM oauth_refresh_tokens r WHERE r.client_id=c.id)
	), stale AS (
		SELECT c.id FROM oauth_clients c JOIN unused u ON u.id=c.id
		WHERE u.created_at<NOW()-interval '24 hours' OR u.position>$1
		FOR UPDATE OF c SKIP LOCKED
	) DELETE FROM oauth_clients c USING stale s WHERE c.id=s.id`, keep)
	return err
}

// The same lock protects authorization/consent references from eviction races.
func lockRegistrations(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(736487232)`)
	return err
}
