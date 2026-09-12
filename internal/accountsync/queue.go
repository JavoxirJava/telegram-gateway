// Package accountsync runs durable, account-routed synchronization. Native RPCs
// occur outside database transactions; every commit checks the current owner.
package accountsync

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	sessionruntime "github.com/JavoxirJava/telegram-gateway/internal/runtime"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Payload struct {
	ChatID         string    `json:"chat_id,omitempty"`
	TelegramChatID int64     `json:"telegram_chat_id,omitempty"`
	MessageID      int64     `json:"message_id,omitempty"`
	MediaID        string    `json:"media_id,omitempty"`
	Before         int64     `json:"before,omitempty"`
	StopAt         int64     `json:"stop_at,omitempty"`
	Cursor         string    `json:"cursor,omitempty"`
	Cycle          string    `json:"cycle,omitempty"`
	Snapshot       time.Time `json:"snapshot,omitempty"`
}
type Job struct {
	ID, Kind, Token string
	Attempts        int
	Payload         Payload
}
type Queue struct {
	pool  *pgxpool.Pool
	lease sessionruntime.Lease
}

func NewQueue(pool *pgxpool.Pool, l sessionruntime.Lease) *Queue { return &Queue{pool: pool, lease: l} }
func (q *Queue) transaction(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='10s'; SET LOCAL lock_timeout='5s'`); err != nil {
		return err
	}
	if err = sessionruntime.FenceTx(ctx, tx, q.lease); err != nil {
		return err
	}
	if err = sessionruntime.LockAccountTx(ctx, tx, q.lease.AccountID); err != nil {
		return err
	}
	if err = fn(ctx, tx); err != nil {
		return err
	}
	if err = sessionruntime.FenceTx(ctx, tx, q.lease); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}
func (q *Queue) enqueue(ctx context.Context, tx pgx.Tx, kind string, p Payload, rearm bool) error {
	switch kind {
	case "chats", "history", "contacts", "members", "media", "refresh", "reconcile":
	default:
		return errors.New("unknown sync job")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(raw) > 4096 {
		return errors.New("job progress is too large")
	}
	digest := sha256.Sum256(raw)
	key := fmt.Sprintf("%x", digest)
	_, err = tx.Exec(ctx, `INSERT INTO gateway_sync_jobs(account_id,generation,kind,dedup_key,payload)
 VALUES($1::uuid,$2,$3,$4,$5::jsonb)
 ON CONFLICT(account_id,generation,kind,dedup_key) DO UPDATE SET
 status=CASE WHEN $6 AND gateway_sync_jobs.status IN ('completed','dead') THEN 'pending' ELSE gateway_sync_jobs.status END,
 attempts=CASE WHEN $6 AND gateway_sync_jobs.status IN ('completed','dead') THEN 0 ELSE gateway_sync_jobs.attempts END,
 reschedule=gateway_sync_jobs.reschedule OR ($6 AND gateway_sync_jobs.status='running'),
 due_at=CASE WHEN $6 AND gateway_sync_jobs.status IN ('completed','dead') THEN NOW() ELSE gateway_sync_jobs.due_at END,
 updated_at=NOW()`, q.lease.AccountID, q.lease.Generation, kind, key, string(raw), rearm)
	return err
}
func (q *Queue) EnqueueTx(ctx context.Context, tx pgx.Tx, kind string, p Payload, rearm bool) error {
	return q.enqueue(ctx, tx, kind, p, rearm)
}
func (q *Queue) Enqueue(ctx context.Context, kind string, p Payload, rearm bool) error {
	return q.transaction(ctx, func(c context.Context, tx pgx.Tx) error { return q.enqueue(c, tx, kind, p, rearm) })
}
func (q *Queue) Start(ctx context.Context) error {
	return q.transaction(ctx, func(c context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(c, `UPDATE gateway_sync_jobs SET status='superseded',claim_token=NULL,claim_until=NULL,updated_at=NOW() WHERE account_id=$1::uuid AND generation<$2 AND status IN ('pending','running')`, q.lease.AccountID, q.lease.Generation); err != nil {
			return err
		}
		return q.enqueue(c, tx, "chats", Payload{Cycle: "startup"}, false)
	})
}
func (q *Queue) Claim(ctx context.Context) (Job, bool, error) {
	var j Job
	var raw []byte
	found := false
	err := q.transaction(ctx, func(c context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(c, `WITH candidate AS (
  SELECT id FROM gateway_sync_jobs WHERE account_id=$1::uuid AND generation=$2
   AND ((status='pending' AND due_at<=NOW()) OR (status='running' AND claim_until<=NOW()))
   ORDER BY due_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
  UPDATE gateway_sync_jobs j SET status='running',claim_token=gen_random_uuid(),claim_until=NOW()+interval '3 minutes',updated_at=NOW()
  FROM candidate c WHERE j.id=c.id RETURNING j.id::text,j.kind,j.claim_token::text,j.attempts,j.payload`, q.lease.AccountID, q.lease.Generation).Scan(&j.ID, &j.Kind, &j.Token, &j.Attempts, &raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return json.Unmarshal(raw, &j.Payload)
	})
	return j, found, err
}
func (q *Queue) Finish(ctx context.Context, j Job, fn func(context.Context, pgx.Tx) error) error {
	return q.transaction(ctx, func(c context.Context, tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(c, `SELECT id::text FROM gateway_sync_jobs WHERE id=$1::uuid AND account_id=$2::uuid AND generation=$3 AND status='running' AND claim_token=$4::uuid AND claim_until>clock_timestamp() FOR UPDATE`, j.ID, q.lease.AccountID, q.lease.Generation, j.Token).Scan(&id); err != nil {
			return err
		}
		if fn != nil {
			if err := fn(c, tx); err != nil {
				return err
			}
		}
		if err := audit.NewWriter(q.pool).WriteTx(c, tx, audit.Event{AccountID: &q.lease.AccountID, ActorType: audit.ActorSystem, ActorID: q.lease.WorkerID, Action: "SYNC_JOB_COMPLETED", ResourceType: "sync_job", ResourceID: j.ID, Metadata: map[string]any{"kind": j.Kind, "generation": q.lease.Generation}}); err != nil {
			return err
		}
		_, err := tx.Exec(c, `UPDATE gateway_sync_jobs SET status=CASE WHEN reschedule THEN 'pending' ELSE 'completed' END,reschedule=FALSE,claim_token=NULL,claim_until=NULL,due_at=NOW(),last_error_code=NULL,updated_at=NOW() WHERE id=$1::uuid`, j.ID)
		return err
	})
}
func (q *Queue) Retry(ctx context.Context, j Job, delay time.Duration, code string, consume, permanent bool) error {
	if delay < time.Second {
		delay = time.Second
	}
	if delay > 30*24*time.Hour {
		delay = 30 * 24 * time.Hour
	}
	if len(code) > 64 {
		return errors.New("invalid error code")
	}
	return q.transaction(ctx, func(c context.Context, tx pgx.Tx) error {
		attempts := j.Attempts
		if consume {
			attempts++
		}
		status := "pending"
		if permanent || attempts >= 10 {
			status = "dead"
		}
		tag, err := tx.Exec(c, `UPDATE gateway_sync_jobs SET status=$5,attempts=$6,due_at=NOW()+$7*interval '1 millisecond',claim_token=NULL,claim_until=NULL,last_error_code=$8,updated_at=NOW() WHERE id=$1::uuid AND account_id=$2::uuid AND generation=$3 AND claim_token=$4::uuid AND status='running'`, j.ID, q.lease.AccountID, q.lease.Generation, j.Token, status, attempts, delay.Milliseconds(), code)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("job claim lost")
		}
		return audit.NewWriter(q.pool).WriteTx(c, tx, audit.Event{AccountID: &q.lease.AccountID, ActorType: audit.ActorSystem, ActorID: q.lease.WorkerID, Action: "SYNC_JOB_" + status, ResourceType: "sync_job", ResourceID: j.ID, Metadata: map[string]any{"kind": j.Kind, "error_code": code, "attempts": attempts}})
	})
}
