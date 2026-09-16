package gateway

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

func Maintain(ctx context.Context, pool *pgxpool.Pool) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		for _, table := range []string{"media_read_tickets", "oauth_codes", "oauth_requests", "oauth_refresh_tokens", "browser_sessions"} {
			run, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, _ = pool.Exec(run, "DELETE FROM "+table+" WHERE expires_at < NOW()")
			cancel()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
