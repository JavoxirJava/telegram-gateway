package runtime

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

var ErrLeaseLost = errors.New("Telegram session ownership is no longer valid")

// FenceTx is held only around persistence, never around network calls. A new
// owner cannot acquire this row until the transaction releases its shared lock.
func FenceTx(ctx context.Context, tx pgx.Tx, l Lease) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM telegram_session_runtime r JOIN telegram_accounts a ON a.id=r.account_id
 WHERE r.account_id=$1::uuid AND r.worker_id=$2 AND r.lease_token=$3::uuid AND r.generation=$4
 AND r.desired_state='online' AND r.lease_expires_at > clock_timestamp() AND a.status='active'
 FOR SHARE OF r,a`, l.AccountID, l.WorkerID, l.Token, l.Generation).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	return err
}

func (r *Repository) Check(ctx context.Context, l Lease) error {
	var one int
	err := r.pool.QueryRow(ctx, `SELECT 1 FROM telegram_session_runtime r JOIN telegram_accounts a ON a.id=r.account_id
 WHERE r.account_id=$1::uuid AND r.worker_id=$2 AND r.lease_token=$3::uuid AND r.generation=$4
 AND r.desired_state='online' AND r.observed_state='ready' AND r.lease_expires_at > clock_timestamp() AND a.status='active'`, l.AccountID, l.WorkerID, l.Token, l.Generation).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	return err
}

// LockAccountTx gives live, historical and retry writers one lock order:
// ownership rows, account lock, then data/job/audit rows. Never hold it over RPCs.
func LockAccountTx(ctx context.Context, tx pgx.Tx, account string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,12))`, account)
	return err
}
