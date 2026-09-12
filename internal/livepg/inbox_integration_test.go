package livepg

import (
	"context"
	"errors"
	"github.com/JavoxirJava/telegram-gateway/internal/tglive"
	"github.com/jackc/pgx/v5"
	"testing"
)

func TestInboxReplayIsAtomicAndIdempotent(t *testing.T) {
	ctx, pool, acct := testDatabase(t)
	s := New(pool)
	event := tglive.Event{Kind: "chat", ChatID: 901, ChatType: "private", Title: "journal"}
	key := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	if e := s.Accept(ctx, acct, key, event); e != nil {
		t.Fatal(e)
	}
	if e := s.Accept(ctx, acct, key, event); e != nil {
		t.Fatal(e)
	}
	s.After = func(context.Context, pgx.Tx, string, tglive.Event) error { return errors.New("fixture failure") }
	if e := s.Replay(ctx, acct); e == nil {
		t.Fatal("failure committed")
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM chats WHERE account_id=$1::uuid`, acct).Scan(&n)
	if n != 0 {
		t.Fatal("partial replay applied")
	}
	s.After = nil
	if e := s.Replay(ctx, acct); e != nil {
		t.Fatal(e)
	}
	if e := s.Replay(ctx, acct); e != nil {
		t.Fatal(e)
	}
	if e := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE account_id=$1::uuid`, acct).Scan(&n); e != nil || n != 1 {
		t.Fatal("duplicate audit", n, e)
	}
}
