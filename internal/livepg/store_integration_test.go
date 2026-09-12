package livepg

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	"github.com/JavoxirJava/telegram-gateway/internal/messages"
	sessionruntime "github.com/JavoxirJava/telegram-gateway/internal/runtime"
	"github.com/JavoxirJava/telegram-gateway/internal/tglive"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testDatabase(t *testing.T) (context.Context, *pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var user, account string
	if err := pool.QueryRow(ctx, `INSERT INTO app_users DEFAULT VALUES RETURNING id::text`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO telegram_accounts(user_id) VALUES($1::uuid) RETURNING id::text`, user).Scan(&account); err != nil {
		t.Fatal(err)
	}
	return ctx, pool, account
}
func TestLiveProjectionDeletionAndAudit(t *testing.T) {
	ctx, pool, account := testDatabase(t)
	store := New(pool)
	apply := func(e tglive.Event) {
		t.Helper()
		if err := store.Apply(ctx, account, e); err != nil {
			t.Fatal(err)
		}
	}
	apply(tglive.Event{Kind: "chat", ChatID: 42, ChatType: "private", Title: "Fixture"})
	var chat string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM chats WHERE account_id=$1::uuid AND telegram_chat_id=42`, account).Scan(&chat); err != nil {
		t.Fatal(err)
	}
	text := "original"
	apply(tglive.Event{Kind: "message", ChatID: 42, MessageID: 100, ContentType: "messageText", Content: &text, Entities: json.RawMessage(`[]`), SentAt: time.Now().UTC()})
	edited := "edited"
	apply(tglive.Event{Kind: "content", ChatID: 42, MessageID: 100, ContentType: "messageText", Content: &edited, Entities: json.RawMessage(`[]`)})
	repo := messages.NewRepository(pool)
	snapshot := messages.Message{AccountID: account, ChatID: chat, TelegramMessageID: 100, MessageType: "messageText", Content: &text, SentAt: time.Now().UTC()}
	if _, err := repo.Upsert(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT content FROM messages WHERE account_id=$1::uuid AND telegram_message_id=100`, account).Scan(&stored); err != nil || stored != edited {
		t.Fatalf("stale history overwrote live edit: %q %v", stored, err)
	}
	apply(tglive.Event{Kind: "deleted", ChatID: 42, MessageIDs: []int64{100, 200}, Permanent: true})
	snapshot.TelegramMessageID = 200
	if _, err := repo.Upsert(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM active_messages WHERE account_id=$1::uuid`, account).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted message leaked: %d %v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE account_id=$1::uuid AND deleted`, account).Scan(&count); err != nil || count != 2 {
		t.Fatal("soft-deleted rows were not retained", err)
	}
	if _, err := audit.NewVerifier(pool).VerifyChain(ctx, account); err != nil {
		t.Fatal(err)
	}
}
func TestLeaseFencesWritesAndRejectsDuplicateOwner(t *testing.T) {
	ctx, pool, account := testDatabase(t)
	r := sessionruntime.NewRepository(pool)
	lease, ok, err := r.Acquire(ctx, account, "worker-a", "local", time.Minute)
	if err != nil || !ok {
		t.Fatal("pending account could not authorize", err)
	}
	if _, ok, err := r.Acquire(ctx, account, "worker-a", "local", time.Minute); err != nil || ok {
		t.Fatal("same worker stole an active lease", err)
	}
	store := NewWithLease(pool, lease)
	if err := store.Apply(ctx, account, tglive.Event{Kind: "chat", ChatID: 71, ChatType: "private"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetDesiredOnline(ctx, account, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Renew(ctx, lease, time.Minute); err == nil {
		t.Fatal("offline account lease renewed")
	}
	if err := store.Apply(ctx, account, tglive.Event{Kind: "title", ChatID: 71, Title: "must not write"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("stale owner wrote live data", err)
	}
}

func TestVerifiedIdentityCannotBeRebound(t *testing.T) {
	ctx, pool, account := testDatabase(t)
	r := sessionruntime.NewRepository(pool)
	lease, ok, err := r.Acquire(ctx, account, "identity-worker", "local", time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := r.ActivateVerified(ctx, lease, 321, "Test", "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := r.ActivateVerified(ctx, lease, 654, "Wrong", "wrong"); err == nil {
		t.Fatal("allowed identity replacement")
	}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT telegram_user_id FROM telegram_accounts WHERE id=$1::uuid`, account).Scan(&id); err != nil || id != 321 {
		t.Fatal("identity binding changed", err)
	}
	if err := r.SetDesiredOnline(ctx, account, false); err != nil {
		t.Fatal(err)
	}
	if err := r.ActivateVerified(ctx, lease, 321, "Wrong", "wrong"); err == nil {
		t.Fatal("offline owner activated identity")
	}
}
