package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ObservedState string

const (
	StateOffline     ObservedState = "offline"
	StateStarting    ObservedState = "starting"
	StateAuthorizing ObservedState = "authorizing"
	StateReady       ObservedState = "ready"
	StateBackoff     ObservedState = "backoff"
	StateError       ObservedState = "error"
)

type Lease struct {
	AccountID  string
	WorkerID   string
	Token      string
	Generation int64
	ExpiresAt  time.Time
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Acquire(ctx context.Context, accountID, workerID, shardKey string, ttl time.Duration) (Lease, bool, error) {
	accountID = strings.TrimSpace(accountID)
	workerID = strings.TrimSpace(workerID)
	shardKey = strings.TrimSpace(shardKey)
	if accountID == "" || workerID == "" {
		return Lease{}, false, errors.New("account id and worker id are required")
	}
	if shardKey == "" {
		shardKey = "default"
	}
	if ttl <= 0 {
		return Lease{}, false, errors.New("lease ttl must be positive")
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Lease{}, false, fmt.Errorf("begin session lease transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO telegram_session_runtime (account_id, shard_key)
		SELECT id, $2
		FROM telegram_accounts
		WHERE id = $1::uuid
		  AND status IN ('pending', 'active')
		ON CONFLICT (account_id) DO NOTHING`, accountID, shardKey); err != nil {
		return Lease{}, false, fmt.Errorf("ensure session runtime row: %w", err)
	}

	var lease Lease
	err = tx.QueryRow(ctx, `
		UPDATE telegram_session_runtime
		SET shard_key = $3,
		    worker_id = $2,
		    lease_token = gen_random_uuid(),
		    lease_expires_at = NOW() + ($4 * interval '1 millisecond'),
		    generation = generation + 1,
		    observed_state = 'starting',
		    last_started_at = NOW(),
		    last_error = NULL,
		    updated_at = NOW()
		WHERE account_id = $1::uuid
		  AND desired_state = 'online'
          AND EXISTS (SELECT 1 FROM telegram_accounts a WHERE a.id=account_id AND a.status IN ('pending','active'))
		  AND (lease_expires_at IS NULL OR lease_expires_at <= NOW())
		RETURNING account_id::text, worker_id, lease_token::text, generation, lease_expires_at`,
		accountID, workerID, shardKey, ttl.Milliseconds(),
	).Scan(&lease.AccountID, &lease.WorkerID, &lease.Token, &lease.Generation, &lease.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, fmt.Errorf("acquire Telegram session lease: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Lease{}, false, fmt.Errorf("commit session lease transaction: %w", err)
	}
	return lease, true, nil
}

func (r *Repository) Renew(ctx context.Context, lease Lease, ttl time.Duration) (time.Time, error) {
	if strings.TrimSpace(lease.AccountID) == "" || strings.TrimSpace(lease.WorkerID) == "" || strings.TrimSpace(lease.Token) == "" {
		return time.Time{}, errors.New("complete lease identity is required")
	}
	if ttl <= 0 {
		return time.Time{}, errors.New("lease ttl must be positive")
	}

	var expiresAt time.Time
	err := r.pool.QueryRow(ctx, `
		UPDATE telegram_session_runtime
		SET lease_expires_at = NOW() + ($4 * interval '1 millisecond'),
		    updated_at = NOW()
		WHERE account_id = $1::uuid
		  AND worker_id = $2
		  AND lease_token = $3::uuid
		  AND generation = $5
		  AND lease_expires_at > NOW()
          AND desired_state='online'
          AND EXISTS (SELECT 1 FROM telegram_accounts a WHERE a.id=account_id AND a.status IN ('pending','active'))
		RETURNING lease_expires_at`,
		lease.AccountID, lease.WorkerID, lease.Token, ttl.Milliseconds(), lease.Generation,
	).Scan(&expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, errors.New("session lease lost")
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("renew Telegram session lease: %w", err)
	}
	return expiresAt, nil
}

func (r *Repository) Release(ctx context.Context, lease Lease) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE telegram_session_runtime
		SET worker_id = NULL,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    observed_state = 'offline',
		    updated_at = NOW()
		WHERE account_id = $1::uuid
		  AND worker_id = $2
		  AND lease_token = $3::uuid
		  AND generation = $4`,
		lease.AccountID, lease.WorkerID, lease.Token, lease.Generation,
	)
	if err != nil {
		return fmt.Errorf("release Telegram session lease: %w", err)
	}
	if result.RowsAffected() == 0 {
		return errors.New("session lease no longer owned by worker")
	}
	return nil
}

func (r *Repository) SetObservedState(ctx context.Context, lease Lease, state ObservedState, lastError string) error {
	if !state.valid() {
		return fmt.Errorf("invalid session observed state %q", state)
	}
	lastError = strings.TrimSpace(lastError)
	result, err := r.pool.Exec(ctx, `
		UPDATE telegram_session_runtime
		SET observed_state = $5,
		    last_error = NULLIF($6, ''),
		    last_ready_at = CASE WHEN $5 = 'ready' THEN NOW() ELSE last_ready_at END,
		    updated_at = NOW()
		WHERE account_id = $1::uuid
		  AND worker_id = $2
		  AND lease_token = $3::uuid
		  AND generation = $4 AND lease_expires_at > NOW() AND desired_state='online'`,
		lease.AccountID, lease.WorkerID, lease.Token, lease.Generation, string(state), lastError,
	)
	if err != nil {
		return fmt.Errorf("update Telegram session state: %w", err)
	}
	if result.RowsAffected() == 0 {
		return errors.New("session lease no longer owned by worker")
	}
	return nil
}

func (r *Repository) SetDesiredOnline(ctx context.Context, accountID string, online bool) error {
	desired := "offline"
	if online {
		desired = "online"
	}
	result, err := r.pool.Exec(ctx, `
		UPDATE telegram_session_runtime
		SET desired_state = $2,
		    updated_at = NOW()
		WHERE account_id = $1::uuid`, strings.TrimSpace(accountID), desired)
	if err != nil {
		return fmt.Errorf("set Telegram session desired state: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s ObservedState) valid() bool {
	switch s {
	case StateOffline, StateStarting, StateAuthorizing, StateReady, StateBackoff, StateError:
		return true
	default:
		return false
	}
}
