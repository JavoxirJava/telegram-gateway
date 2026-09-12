package accountsync

import (
	"context"
	"errors"
	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	sessionruntime "github.com/JavoxirJava/telegram-gateway/internal/runtime"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"time"
)

func queueFixture(t *testing.T) (context.Context, *Queue) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	var user, acct string
	if e = pool.QueryRow(ctx, `INSERT INTO app_users DEFAULT VALUES RETURNING id::text`).Scan(&user); e != nil {
		t.Fatal(e)
	}
	if e = pool.QueryRow(ctx, `INSERT INTO telegram_accounts(user_id,status) VALUES($1::uuid,'active') RETURNING id::text`, user).Scan(&acct); e != nil {
		t.Fatal(e)
	}
	l, ok, e := sessionruntime.NewRepository(pool).Acquire(ctx, acct, "queue-fixture", "local", time.Minute)
	if e != nil || !ok {
		t.Fatal(e)
	}
	return ctx, NewQueue(pool, l)
}
func TestQueueClaimCommitAndAudit(t *testing.T) {
	ctx, q := queueFixture(t)
	if e := q.Start(ctx); e != nil {
		t.Fatal(e)
	}
	if e := q.Start(ctx); e != nil {
		t.Fatal(e)
	}
	j, ok, e := q.Claim(ctx)
	if e != nil || !ok || j.Kind != "chats" {
		t.Fatal(j, ok, e)
	}
	if e = q.Finish(ctx, j, nil); e != nil {
		t.Fatal(e)
	}
	_, ok, e = q.Claim(ctx)
	if e != nil || ok {
		t.Fatal("dedup failed", ok, e)
	}
	if _, e = audit.NewVerifier(q.pool).VerifyChain(ctx, q.lease.AccountID); e != nil {
		t.Fatal(e)
	}
}
func TestQueueRejectsStaleLeaseAndRollsBack(t *testing.T) {
	ctx, q := queueFixture(t)
	if e := q.Start(ctx); e != nil {
		t.Fatal(e)
	}
	j, ok, e := q.Claim(ctx)
	if e != nil || !ok {
		t.Fatal(e)
	}
	sentinel := errors.New("test rollback")
	e = q.Finish(ctx, j, func(c context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(c, `INSERT INTO chats(account_id,telegram_chat_id,chat_type) VALUES($1::uuid,77,'private')`, q.lease.AccountID)
		if e != nil {
			return e
		}
		return sentinel
	})
	if !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
	var n int
	if e = q.pool.QueryRow(ctx, `SELECT count(*) FROM chats WHERE account_id=$1::uuid`, q.lease.AccountID).Scan(&n); e != nil || n != 0 {
		t.Fatal("partial commit", n, e)
	}
	if e = sessionruntime.NewRepository(q.pool).SetDesiredOnline(ctx, q.lease.AccountID, false); e != nil {
		t.Fatal(e)
	}
	if e = q.Finish(ctx, j, nil); !errors.Is(e, sessionruntime.ErrLeaseLost) {
		t.Fatal("stale owner accepted", e)
	}
}
func TestThrottlingDoesNotExhaustRetryBudget(t *testing.T) {
	ctx, q := queueFixture(t)
	q.Start(ctx)
	j, ok, e := q.Claim(ctx)
	if e != nil || !ok {
		t.Fatal(e)
	}
	if e = q.Retry(ctx, j, time.Minute, "flood_wait", false, false); e != nil {
		t.Fatal(e)
	}
	var attempts int
	var status string
	if e = q.pool.QueryRow(ctx, `SELECT attempts,status FROM gateway_sync_jobs WHERE id=$1::uuid`, j.ID).Scan(&attempts, &status); e != nil || attempts != 0 || status != "pending" {
		t.Fatal(attempts, status, e)
	}
}
func TestWrongClaimCannotCommit(t *testing.T) {
	ctx, q := queueFixture(t)
	q.Start(ctx)
	j, ok, e := q.Claim(ctx)
	if e != nil || !ok {
		t.Fatal(e)
	}
	old := j
	j.Token = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	if e = q.Finish(ctx, j, nil); e == nil {
		t.Fatal("wrong claim accepted")
	}
	if e = q.Finish(ctx, old, nil); e != nil {
		t.Fatal(e)
	}
}
