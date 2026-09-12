package accountsync

import (
	"context"
	"errors"
	"github.com/JavoxirJava/telegram-gateway/internal/objectstore"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

type emptyNative struct{}

func (emptyNative) Profile(context.Context) (telegram.Profile, error) { return telegram.Profile{}, nil }
func (emptyNative) ListChats(context.Context, string, int) (telegram.ChatPage, error) {
	return telegram.ChatPage{Items: []telegram.Chat{}}, nil
}
func (emptyNative) GetChatHistory(context.Context, int64, int64, int) (telegram.HistoryPage, error) {
	return telegram.HistoryPage{}, errors.New("unexpected history")
}
func (emptyNative) ListContacts(context.Context) ([]telegram.Contact, error) {
	return []telegram.Contact{}, nil
}
func (emptyNative) ListMembers(context.Context, int64, string, int) (telegram.MemberPage, error) {
	return telegram.MemberPage{}, errors.New("unexpected members")
}
func (emptyNative) DownloadFile(context.Context, int64) (telegram.Download, error) {
	return telegram.Download{}, errors.New("unexpected download")
}
func (emptyNative) Message(context.Context, int64, int64) (telegram.Message, error) {
	return telegram.Message{}, errors.New("unexpected message")
}
func (emptyNative) Messages(context.Context, int64, []int64) (map[int64]telegram.Message, []int64, error) {
	return nil, nil, errors.New("unexpected messages")
}
func TestRunnerNeverConsumesAnotherAccountsJobs(t *testing.T) {
	u := os.Getenv("TEST_NATS_URL")
	if u == "" {
		t.Skip("TEST_NATS_URL not set")
	}
	ctx, q := queueFixture(t)
	_, other := queueFixture(t)
	if e := other.Start(ctx); e != nil {
		t.Fatal(e)
	}
	nc, e := nats.Connect(u)
	if e != nil {
		t.Fatal(e)
	}
	defer nc.Close()
	js, e := jetstream.New(nc)
	if e != nil {
		t.Fatal(e)
	}
	r, e := NewRunner(q, emptyNative{}, &objectstore.Store{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	run, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- r.Run(run, nc, js, make(chan struct{})) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(10 * time.Second)
	complete := false
	for time.Now().Before(deadline) {
		var n int
		e = q.pool.QueryRow(ctx, `SELECT count(*) FROM gateway_sync_jobs WHERE account_id=$1::uuid AND status='completed'`, q.lease.AccountID).Scan(&n)
		if e != nil {
			t.Fatal(e)
		}
		if n >= 2 {
			complete = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !complete {
		t.Fatal("account runner did not complete bootstrap")
	}
	var n int
	if e = q.pool.QueryRow(ctx, `SELECT count(*) FROM gateway_sync_jobs WHERE account_id=$1::uuid AND status='pending'`, other.lease.AccountID).Scan(&n); e != nil || n != 1 {
		t.Fatal("cross-account consumption", n, e)
	}
}
