package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/tdlib"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (m *Manager) NewLoginSession(ctx context.Context, storageID string) (telegram.LoginSession, error) {
	if _, err := uuid.Parse(storageID); err != nil {
		return nil, err
	}
	return tdlib.Open(ctx, tdlib.Options{AccountID: storageID, APIID: m.config.APIID, APIHash: m.config.APIHash, MasterKey: m.key, Directory: m.config.DataDir})
}

// AttachLogin is called only with getMe from a successfully authenticated native
// Telegram session. A submitted phone number or Telegram ID never selects a user.
// The login session must be closed before handing its encrypted directory over.
func (m *Manager) AttachLogin(ctx context.Context, storageID string, profile telegram.Profile) (string, string, error) {
	if _, err := uuid.Parse(storageID); err != nil || profile.TelegramUserID <= 0 {
		return "", "", errors.New("invalid verified identity")
	}
	m.loginMu.Lock()
	defer m.loginMu.Unlock()
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, profile.TelegramUserID); err != nil {
		return "", "", err
	}
	var account, user, status string
	err = tx.QueryRow(ctx, `SELECT ta.id::text,ta.user_id::text,ta.status FROM telegram_identities i JOIN telegram_accounts ta ON ta.id=i.account_id WHERE i.telegram_user_id=$1`, profile.TelegramUserID).Scan(&account, &user, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		// Includes accounts connected by the administrator after the migration.
		err = tx.QueryRow(ctx, `SELECT id::text,user_id::text,status FROM telegram_accounts WHERE telegram_user_id=$1 ORDER BY created_at,id LIMIT 1`, profile.TelegramUserID).Scan(&account, &user, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			if err = tx.QueryRow(ctx, `INSERT INTO app_users DEFAULT VALUES RETURNING id::text`).Scan(&user); err != nil {
				return "", "", err
			}
			account = uuid.NewString()
			status = "pending"
			_, err = tx.Exec(ctx, `INSERT INTO telegram_accounts(id,user_id,telegram_user_id,status,session_directory_id) VALUES($1::uuid,$2::uuid,$3,'pending',$4::uuid)`, account, user, profile.TelegramUserID, storageID)
		}
		if err != nil {
			return "", "", err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO telegram_identities(telegram_user_id,account_id) VALUES($1,$2::uuid)`, profile.TelegramUserID, account); err != nil {
			return "", "", err
		}
	}
	if err != nil {
		return "", "", err
	}
	if status == "blocked" {
		return "", "", errors.New("account is unavailable")
	}
	_, err = tx.Exec(ctx, `INSERT INTO telegram_session_runtime(account_id,desired_state) VALUES($1::uuid,'offline') ON CONFLICT(account_id) DO UPDATE SET desired_state='offline'`, account)
	if err != nil {
		return "", "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", err
	}
	m.mu.RLock()
	old := m.sessions[account]
	m.mu.RUnlock()
	if old != nil {
		old.Close()
	}
	wait, stop := context.WithTimeout(ctx, 12*time.Second)
	defer stop()
	for {
		var free bool
		if err = m.pool.QueryRow(wait, `SELECT lease_expires_at IS NULL OR lease_expires_at<NOW() FROM telegram_session_runtime WHERE account_id=$1::uuid`, account).Scan(&free); err != nil {
			return "", "", err
		}
		if free {
			break
		}
		select {
		case <-wait.Done():
			return "", "", wait.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	tx, err = m.pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(context.Background())
	tag, err := tx.Exec(ctx, `UPDATE telegram_accounts SET session_directory_id=$2::uuid,display_name=NULLIF($3,''),username=NULLIF($4,''),status='active',connected_at=COALESCE(connected_at,NOW()),disconnected_at=NULL,updated_at=NOW() WHERE id=$1::uuid AND telegram_user_id=$5 AND status<>'blocked'`, account, storageID, profile.DisplayName, profile.Username, profile.TelegramUserID)
	if err != nil {
		return "", "", err
	}
	if tag.RowsAffected() != 1 {
		return "", "", errors.New("account unavailable")
	}
	if _, err = tx.Exec(ctx, `UPDATE telegram_session_runtime SET desired_state='online' WHERE account_id=$1::uuid`, account); err != nil {
		return "", "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return user, account, nil
}

func (m *Manager) DiscardLoginStorage(ctx context.Context, id string) {
	if m.config.DataDir == "" {
		return
	}
	if _, err := uuid.Parse(id); err != nil {
		return
	}
	var used bool
	if err := m.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM telegram_accounts WHERE session_directory_id=$1::uuid)`, id).Scan(&used); err != nil || used {
		return
	}
	_ = os.RemoveAll(filepath.Join(m.config.DataDir, id))
}
