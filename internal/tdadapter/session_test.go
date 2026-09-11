package tdadapter

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/tdjson"
	"github.com/JavoxirJava/telegram-gateway/internal/tdjson/tdjsontest"
	"github.com/JavoxirJava/telegram-gateway/internal/tdlib"
)

type countingPolicy struct{ calls atomic.Int32 }

func (p *countingPolicy) Before(context.Context, string) error     { p.calls.Add(1); return nil }
func (p *countingPolicy) After(_ context.Context, err error) error { return err }

// Real Go TDLib routing and policy; only the native transport is a test double.
func TestGovernedSessionAndOrderedChatIndex(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tr := tdjsontest.New()
	tr.OnSend = func(id int, r map[string]any) {
		switch r["@type"] {
		case "getAuthorizationState":
			tr.State(id, tdlib.Ready)
			tr.Reply(id, r, map[string]any{"@type": tdlib.Ready})
		case "loadChats":
			tr.Emit(id, map[string]any{"@type": "updateNewChat", "chat": map[string]any{"id": 10, "title": "ordered", "type": map[string]any{"@type": "chatTypePrivate"}, "positions": []any{map[string]any{"list": map[string]any{"@type": "chatListMain"}, "order": "9007199254740993"}}}})
			tr.Reply(id, r, map[string]any{"@type": "ok"})
		default:
			tr.DefaultSend(id, r)
		}
	}
	engine, err := tdjson.New(tr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		if err := engine.Close(closeCtx); err != nil {
			t.Error(err)
		}
	}()
	index := NewIndex()
	policy := &countingPolicy{}
	root := t.TempDir()
	session, err := tdlib.New(engine, tdlib.Config{APIID: 123, APIHash: "0123456789abcdef0123456789abcdef", DatabaseDirectory: root, FilesDirectory: root, DatabaseKey: make([]byte, 32), Requests: policy}, index.Observe)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	var verified atomic.Bool
	denied := errors.New("identity not verified")
	adapter, err := New(session, index, Config{AccountID: accountID, FilesDirectory: root, MaxFileBytes: 1024, Authorize: func(context.Context) error {
		if !verified.Load() {
			return denied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if _, err := adapter.ListChats(ctx, "", 10); !errors.Is(err, denied) || policy.calls.Load() != 0 {
		t.Fatal("unverified read", err, policy.calls.Load())
	}
	verified.Store(true)
	p, err := adapter.ListChats(ctx, "", 10)
	if err != nil || len(p.Items) != 1 || p.Items[0].Title != "ordered" || policy.calls.Load() != 1 {
		t.Fatal(p, err, policy.calls.Load())
	}
	if _, err := session.Read(ctx, "sendMessage", map[string]any{"chat_id": 10}); !errors.Is(err, tdjson.ErrReadOnly) {
		t.Fatal("write allowlist bypass", err)
	}
	// A malformed native collection is not converted to a successful empty page.
	if err := index.Observe(ctx, json.RawMessage(`{"@type":"updateNewChat","chat":null}`)); !errors.Is(err, ErrInvalidResponse) {
		t.Fatal(err)
	}
}
