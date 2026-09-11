package syncstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const zeroUUID = "00000000-0000-0000-0000-000000000000"

type State struct {
	ID              string
	AccountID       string
	ChatID          *string
	SyncType        string
	Status          string
	Cursor          map[string]any
	OldestMessageID *int64
	NewestMessageID *int64
	LastSyncedAt    *time.Time
	NextSyncAt      *time.Time
	RetryCount      int
	LastError       *string
	Generation      int64
}

type Lease struct {
	StateID    string
	Owner      string
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

func (r *Repository) Ensure(ctx context.Context, accountID string, chatID *string, syncType string) (State, error) {
	accountID = strings.TrimSpace(accountID)
	syncType = strings.TrimSpace(syncType)
	if accountID == "" || syncType == "" {
		return State{}, errors.New("account id and sync type are required")
	}

	var chatValue any
	if chatID != nil {
		trimmed := strings.TrimSpace(*chatID)
		if trimmed == "" {
			return State{}, errors.New("chat id cannot be empty")
		}
		chatValue = trimmed
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO sync_states (account_id, chat_id, sync_type, status)
		VALUES ($1::uuid, $2::uuid, $3, 'pending')
		ON CONFLICT DO NOTHING`, accountID, chatValue, syncType)
	if err != nil {
		return State{}, fmt.Errorf("ensure sync state: %w", err)
	}

	return r.getByScope(ctx, accountID, chatValue, syncType)
}

func (r *Repository) Acquire(ctx context.Context, stateID, owner string, ttl time.Duration) (Lease, bool, error) {
	stateID = strings.TrimSpace(stateID)
	owner = strings.TrimSpace(owner)
	if stateID == "" || owner == "" {
		return Lease{}, false, errors.New("state id and lease owner are required")
	}
	if ttl <= 0 {
		return Lease{}, false, errors.New("lease ttl must be positive")
	}

	var lease Lease
	err := r.pool.QueryRow(ctx, `
		UPDATE sync_states
		SET lease_owner = $2,
		    lease_token = gen_random_uuid(),
		    lease_expires_at = NOW() + ($3 * interval '1 millisecond'),
		    generation = generation + 1,
		    status = 'running',
		    last_error = NULL,
		    updated_at = NOW()
		WHERE id = $1::uuid
		  AND (lease_expires_at IS NULL OR lease_expires_at <= NOW() OR lease_owner = $2)
		RETURNING id::text, lease_owner, lease_token::text, generation, lease_expires_at`,
		stateID, owner, ttl.Milliseconds(),
	).Scan(&lease.StateID, &lease.Owner, &lease.Token, &lease.Generation, &lease.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, fmt.Errorf("acquire sync state lease: %w", err)
	}
	return lease, true, nil
}

func (r *Repository) Progress(ctx context.Context, lease Lease, cursor map[string]any, oldestMessageID, newestMessageID *int64) error {
	if cursor == nil {
		cursor = map[string]any{}
	}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return fmt.Errorf("marshal sync cursor: %w", err)
	}

	result, err := r.pool.Exec(ctx, `
		UPDATE sync_states
		SET cursor = $5::jsonb,
		    oldest_message_id = COALESCE($6, oldest_message_id),
		    newest_message_id = COALESCE($7, newest_message_id),
		    last_synced_at = NOW(),
		    updated_at = NOW()
		WHERE id = $1::uuid
		  AND lease_owner = $2
		  AND lease_token = $3::uuid
		  AND generation = $4
		  AND lease_expires_at > NOW()`,
		lease.StateID, lease.Owner, lease.Token, lease.Generation, string(encoded), oldestMessageID, newestMessageID,
	)
	if err != nil {
		return fmt.Errorf("update sync progress: %w", err)
	}
	if result.RowsAffected() == 0 {
		return errors.New("sync state lease lost")
	}
	return nil
}

func (r *Repository) Complete(ctx context.Context, lease Lease) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE sync_states
		SET status = 'completed',
		    last_synced_at = NOW(),
		    next_sync_at = NULL,
		    retry_count = 0,
		    last_error = NULL,
		    lease_owner = NULL,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    updated_at = NOW()
		WHERE id = $1::uuid
		  AND lease_owner = $2
		  AND lease_token = $3::uuid
		  AND generation = $4`,
		lease.StateID, lease.Owner, lease.Token, lease.Generation,
	)
	if err != nil {
		return fmt.Errorf("complete sync state: %w", err)
	}
	if result.RowsAffected() == 0 {
		return errors.New("sync state lease lost")
	}
	return nil
}

func (r *Repository) Fail(ctx context.Context, lease Lease, cause error, retryAfter time.Duration) error {
	message := "sync failed"
	if cause != nil {
		message = cause.Error()
	}
	if len(message) > 4000 {
		message = message[:4000]
	}
	if retryAfter < 0 {
		retryAfter = 0
	}

	result, err := r.pool.Exec(ctx, `
		UPDATE sync_states
		SET status = 'failed',
		    retry_count = retry_count + 1,
		    last_error = $5,
		    next_sync_at = NOW() + ($6 * interval '1 millisecond'),
		    lease_owner = NULL,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    updated_at = NOW()
		WHERE id = $1::uuid
		  AND lease_owner = $2
		  AND lease_token = $3::uuid
		  AND generation = $4`,
		lease.StateID, lease.Owner, lease.Token, lease.Generation, message, retryAfter.Milliseconds(),
	)
	if err != nil {
		return fmt.Errorf("fail sync state: %w", err)
	}
	if result.RowsAffected() == 0 {
		return errors.New("sync state lease lost")
	}
	return nil
}

func (r *Repository) getByScope(ctx context.Context, accountID string, chatValue any, syncType string) (State, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id::text, account_id::text, chat_id::text, sync_type, status, cursor,
		       oldest_message_id, newest_message_id, last_synced_at, next_sync_at,
		       retry_count, last_error, generation
		FROM sync_states
		WHERE account_id = $1::uuid
		  AND COALESCE(chat_id, $4::uuid) = COALESCE($2::uuid, $4::uuid)
		  AND sync_type = $3`, accountID, chatValue, syncType, zeroUUID)

	var state State
	var cursorJSON []byte
	if err := row.Scan(
		&state.ID,
		&state.AccountID,
		&state.ChatID,
		&state.SyncType,
		&state.Status,
		&cursorJSON,
		&state.OldestMessageID,
		&state.NewestMessageID,
		&state.LastSyncedAt,
		&state.NextSyncAt,
		&state.RetryCount,
		&state.LastError,
		&state.Generation,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return State{}, pgx.ErrNoRows
		}
		return State{}, fmt.Errorf("get sync state: %w", err)
	}
	if len(cursorJSON) > 0 {
		_ = json.Unmarshal(cursorJSON, &state.Cursor)
	}
	return state, nil
}
