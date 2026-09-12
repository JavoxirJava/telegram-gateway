package accountsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	"github.com/JavoxirJava/telegram-gateway/internal/livepg"
	"github.com/JavoxirJava/telegram-gateway/internal/objectstore"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/JavoxirJava/telegram-gateway/internal/tglive"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type historyFixtureNative struct {
	emptyNative
	page telegram.HistoryPage
}

func (n historyFixtureNative) GetChatHistory(context.Context, int64, int64, int) (telegram.HistoryPage, error) {
	return n.page, nil
}

func TestHistoryPersistsProgressMediaAndDeletionGuards(t *testing.T) {
	ctx, q := queueFixture(t)
	store := livepg.NewWithLease(q.pool, q.lease)
	if err := store.Apply(ctx, q.lease.AccountID, tglive.Event{Kind: "chat", ChatID: 88, ChatType: "private"}); err != nil {
		t.Fatal(err)
	}
	var chat string
	if err := q.pool.QueryRow(ctx, `SELECT id::text FROM chats WHERE account_id=$1::uuid AND telegram_chat_id=88`, q.lease.AccountID).Scan(&chat); err != nil {
		t.Fatal(err)
	}
	text := "history fixture"
	size := int64(9)
	native := historyFixtureNative{page: telegram.HistoryPage{Items: []telegram.Message{
		{TelegramMessageID: 102, Type: "messageDocument", Content: &text, SentAt: time.Now().UTC(), Media: []telegram.Media{{Type: "document", TelegramFileID: 17, UniqueFileKey: "stable-fixture", FileSize: &size}}},
		{TelegramMessageID: 101, Type: "messageText", Content: &text, SentAt: time.Now().UTC()},
	}, SourceCount: 2, NextBeforeMessageID: 101}}
	r, err := NewRunner(q, native, &objectstore.Store{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Enqueue(ctx, "history", Payload{ChatID: chat, TelegramChatID: 88}, false); err != nil {
		t.Fatal(err)
	}
	j, ok, err := q.Claim(ctx)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = r.perform(ctx, j); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = q.pool.QueryRow(ctx, `SELECT count(*) FROM active_messages WHERE account_id=$1::uuid`, q.lease.AccountID).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	if err = q.pool.QueryRow(ctx, `SELECT count(*) FROM active_message_media WHERE account_id=$1::uuid`, q.lease.AccountID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	var before int64
	var exhausted bool
	if err = q.pool.QueryRow(ctx, `SELECT before_message_id,exhausted FROM gateway_history_progress WHERE account_id=$1::uuid AND chat_id=$2::uuid`, q.lease.AccountID, chat).Scan(&before, &exhausted); err != nil || before != 101 || exhausted {
		t.Fatal(before, exhausted, err)
	}
	if err = store.Apply(ctx, q.lease.AccountID, tglive.Event{Kind: "deleted", ChatID: 88, MessageIDs: []int64{102}, Permanent: true}); err != nil {
		t.Fatal(err)
	}
	if err = q.pool.QueryRow(ctx, `SELECT count(*) FROM active_message_media WHERE account_id=$1::uuid`, q.lease.AccountID).Scan(&count); err != nil || count != 0 {
		t.Fatal("deleted attachment exposed", count, err)
	}
	if err = q.pool.QueryRow(ctx, `SELECT count(*) FROM message_media mm JOIN messages m ON m.id=mm.message_id WHERE m.account_id=$1::uuid AND m.deleted`, q.lease.AccountID).Scan(&count); err != nil || count != 1 {
		t.Fatal("retention changed", count, err)
	}
	// A newer live edit defeats a stale reconciliation snapshot and its attachments.
	newer := "newer live fixture"
	if err = store.Apply(ctx, q.lease.AccountID, tglive.Event{Kind: "content", ChatID: 88, MessageID: 101, ContentType: "messageText", Content: &newer, Entities: json.RawMessage(`[]`)}); err != nil {
		t.Fatal(err)
	}
	stale := native.page.Items[1]
	if err = q.transaction(ctx, func(c context.Context, tx pgx.Tx) error { return r.applyFresh(c, tx, chat, 101, 0, &stale) }); err != nil {
		t.Fatal(err)
	}
	var got string
	if err = q.pool.QueryRow(ctx, `SELECT content FROM messages WHERE account_id=$1::uuid AND telegram_message_id=101`, q.lease.AccountID).Scan(&got); err != nil || got != newer {
		t.Fatal(got, err)
	}
	if _, err = audit.NewVerifier(q.pool).VerifyChain(ctx, q.lease.AccountID); err != nil {
		t.Fatal(err)
	}
}

func TestAPIRoleHasNoRetainedBodyOrJournalAccess(t *testing.T) {
	ctx, q := queueFixture(t)
	var roles bool
	if err := q.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='gateway_api')`).Scan(&roles); err != nil {
		t.Fatal(err)
	}
	if !roles {
		t.Skip("deployment roles not installed")
	}
	// Run inside rollback-only transactions; the API role is not a table owner.
	for _, query := range []string{`SELECT content FROM messages LIMIT 1`, `SELECT payload FROM gateway_live_inbox LIMIT 1`, `SELECT * FROM chats LIMIT 1`} {
		tx, err := q.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `SET LOCAL ROLE gateway_api`); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, query)
		tx.Rollback(ctx)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
			t.Fatalf("base access not denied: %s: %v", query, err)
		}
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL ROLE gateway_api`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT content FROM active_messages LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT received_at,applied_at FROM gateway_live_inbox LIMIT 1`); err != nil {
		t.Fatal(err)
	}
}
