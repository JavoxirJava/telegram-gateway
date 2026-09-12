package runtime

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	"github.com/jackc/pgx/v5"
)

// ActivateVerified binds an authenticated native identity without allowing an
// expired owner or a different Telegram user to replace an existing binding.
func (r *Repository) ActivateVerified(ctx context.Context, lease Lease, telegramUserID int64, displayName, username string) error {
	if telegramUserID <= 0 {
		return errors.New("invalid verified Telegram identity")
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var marker int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM telegram_session_runtime
      WHERE account_id=$1::uuid AND worker_id=$2 AND lease_token=$3::uuid AND generation=$4
        AND desired_state='online' AND lease_expires_at > clock_timestamp()
      FOR UPDATE`, lease.AccountID, lease.WorkerID, lease.Token, lease.Generation).Scan(&marker); err != nil {
		return errors.New("session ownership could not be verified during activation")
	}
	result, err := tx.Exec(ctx, `UPDATE telegram_accounts
      SET telegram_user_id=$2,display_name=NULLIF($3,''),username=NULLIF($4,''),status='active',
          connected_at=COALESCE(connected_at,NOW()),disconnected_at=NULL,last_update_at=NOW(),updated_at=NOW()
      WHERE id=$1::uuid AND status IN ('pending','active')
        AND (telegram_user_id IS NULL OR telegram_user_id=$2)`,
		lease.AccountID, telegramUserID, strings.TrimSpace(displayName), strings.TrimSpace(username))
	if err != nil {
		return errors.New("cannot bind verified Telegram identity")
	}
	if result.RowsAffected() != 1 {
		return errors.New("Telegram identity binding is unavailable or does not match")
	}
	if err := audit.NewWriter(r.pool).WriteTx(ctx, tx, audit.Event{
		AccountID: &lease.AccountID, ActorType: audit.ActorSystem, ActorID: lease.WorkerID,
		Action: "TELEGRAM_IDENTITY_VERIFIED", ResourceType: "telegram_account", ResourceID: lease.AccountID,
		Metadata: map[string]any{"telegram_user_id": strconv.FormatInt(telegramUserID, 10)},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
