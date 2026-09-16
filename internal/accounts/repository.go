package accounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Status string

const (
	StatusPending      Status = "pending"
	StatusActive       Status = "active"
	StatusDisconnected Status = "disconnected"
	StatusBlocked      Status = "blocked"
)

type Account struct {
	ID             string     `json:"id"`
	UserID         string     `json:"user_id"`
	TelegramUserID *int64     `json:"telegram_user_id,omitempty"`
	DisplayName    *string    `json:"display_name,omitempty"`
	Username       *string    `json:"username,omitempty"`
	Status         Status     `json:"status"`
	ConnectedAt    *time.Time `json:"connected_at,omitempty"`
	DisconnectedAt *time.Time `json:"disconnected_at,omitempty"`
	LastUpdateAt   *time.Time `json:"last_update_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) CreatePending(ctx context.Context, userID string) (Account, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return Account{}, errors.New("user id is required")
	}

	row := r.pool.QueryRow(ctx, `
		INSERT INTO telegram_accounts (user_id, status, session_directory_id)
		VALUES ($1::uuid, 'pending', gen_random_uuid())
		RETURNING id::text, user_id::text, telegram_user_id, display_name, username,
		          status, connected_at, disconnected_at, last_update_at, created_at, updated_at`, userID)
	return scanAccount(row)
}

func (r *Repository) Activate(ctx context.Context, accountID string, telegramUserID int64, displayName, username string) (Account, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return Account{}, errors.New("account id is required")
	}
	if telegramUserID <= 0 {
		return Account{}, errors.New("telegram user id must be positive")
	}

	displayName = strings.TrimSpace(displayName)
	username = strings.TrimSpace(username)
	row := r.pool.QueryRow(ctx, `
		UPDATE telegram_accounts
		SET telegram_user_id = $2,
		    display_name = NULLIF($3, ''),
		    username = NULLIF($4, ''),
		    status = 'active',
		    connected_at = COALESCE(connected_at, NOW()),
		    disconnected_at = NULL,
		    last_update_at = NOW(),
		    updated_at = NOW()
		WHERE id = $1::uuid
		  AND status <> 'blocked'
		RETURNING id::text, user_id::text, telegram_user_id, display_name, username,
		          status, connected_at, disconnected_at, last_update_at, created_at, updated_at`,
		accountID, telegramUserID, displayName, username)
	return scanAccount(row)
}

func (r *Repository) MarkDisconnected(ctx context.Context, accountID string) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE telegram_accounts
		SET status = 'disconnected',
		    disconnected_at = COALESCE(disconnected_at, NOW()),
		    updated_at = NOW()
		WHERE id = $1::uuid
		  AND status <> 'blocked'`, strings.TrimSpace(accountID))
	if err != nil {
		return fmt.Errorf("mark Telegram account disconnected: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *Repository) TouchUpdate(ctx context.Context, accountID string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	result, err := r.pool.Exec(ctx, `
		UPDATE telegram_accounts
		SET last_update_at = $2,
		    updated_at = NOW()
		WHERE id = $1::uuid
		  AND status = 'active'`, strings.TrimSpace(accountID), at.UTC())
	if err != nil {
		return fmt.Errorf("touch Telegram account update time: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, accountID string) (Account, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id::text, user_id::text, telegram_user_id, display_name, username,
		       status, connected_at, disconnected_at, last_update_at, created_at, updated_at
		FROM telegram_accounts
		WHERE id = $1::uuid`, strings.TrimSpace(accountID))
	return scanAccount(row)
}

func (r *Repository) GetActive(ctx context.Context, accountID string) (Account, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id::text, user_id::text, telegram_user_id, display_name, username,
		       status, connected_at, disconnected_at, last_update_at, created_at, updated_at
		FROM telegram_accounts
		WHERE id = $1::uuid
		  AND status = 'active'`, strings.TrimSpace(accountID))
	return scanAccount(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAccount(row rowScanner) (Account, error) {
	var account Account
	var status string
	if err := row.Scan(
		&account.ID,
		&account.UserID,
		&account.TelegramUserID,
		&account.DisplayName,
		&account.Username,
		&status,
		&account.ConnectedAt,
		&account.DisconnectedAt,
		&account.LastUpdateAt,
		&account.CreatedAt,
		&account.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Account{}, pgx.ErrNoRows
		}
		return Account{}, fmt.Errorf("scan Telegram account: %w", err)
	}
	account.Status = Status(status)
	return account, nil
}
