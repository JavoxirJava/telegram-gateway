package tdjson_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/tdjson"
	"github.com/JavoxirJava/telegram-gateway/internal/tdjson/tdjsontest"
)

func engine(t *testing.T, transport *tdjsontest.Transport) *tdjson.Engine {
	t.Helper()
	e, err := tdjson.New(transport)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = e.Close(ctx)
	})
	return e
}
func TestConcurrentRequestCorrelation(t *testing.T) {
	tr := tdjsontest.New()
	tr.OnSend = func(id int, r map[string]any) {
		if r["@type"] == "close" {
			tr.DefaultSend(id, r)
			return
		}
		go tr.Reply(id, r, map[string]any{"@type": "user", "id": r["user_id"]})
	}
	e := engine(t, tr)
	a, _ := e.NewClient(nil)
	b, _ := e.NewClient(nil)
	var wg sync.WaitGroup
	for i := 1; i <= 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := a
			if i%2 == 0 {
				c = b
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			raw, err := c.Call(ctx, "getUser", map[string]any{"user_id": i})
			if err != nil {
				t.Error(err)
				return
			}
			var v struct {
				ID int `json:"id"`
			}
			_ = json.Unmarshal(raw, &v)
			if v.ID != i {
				t.Errorf("reply %d went to request %d", v.ID, i)
			}
		}(i)
	}
	wg.Wait()
	if tr.ConcurrentReceive.Load() {
		t.Fatal("concurrent td_receive")
	}
}
func TestUpdateCommitsBeforeFollowingReply(t *testing.T) {
	tr := tdjsontest.New()
	entered := make(chan struct{})
	release := make(chan struct{})
	tr.OnSend = func(id int, r map[string]any) {
		if r["@type"] == "close" {
			tr.DefaultSend(id, r)
			return
		}
		tr.Emit(id, map[string]any{"@type": "updateTest"})
		tr.Reply(id, r, map[string]any{"@type": "ok"})
	}
	e := engine(t, tr)
	c, _ := e.NewClient(func(ctx context.Context, raw json.RawMessage) error {
		if strings.Contains(string(raw), "updateTest") {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	done := make(chan error, 1)
	go func() { _, err := c.Call(context.Background(), "getMe", nil); done <- err }()
	<-entered
	select {
	case <-done:
		t.Fatal("response overtook an uncommitted update")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestCancellationAndLateResponse(t *testing.T) {
	tr := tdjsontest.New()
	saved := make(chan map[string]any, 1)
	tr.OnSend = func(id int, r map[string]any) {
		if r["@type"] == "close" {
			tr.DefaultSend(id, r)
		} else {
			saved <- r
		}
	}
	e := engine(t, tr)
	c, _ := e.NewClient(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Call(ctx, "getMe", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	old := <-saved
	tr.Reply(1, old, map[string]any{"@type": "user", "id": 123})
	done := make(chan error, 1)
	go func() {
		raw, err := c.Call(context.Background(), "getMe", nil)
		if err == nil && !strings.Contains(string(raw), "456") {
			err = errors.New("late reply matched new call")
		}
		done <- err
	}()
	fresh := <-saved
	tr.Reply(1, fresh, map[string]any{"@type": "user", "id": 456})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestReadOnlyAndReservedFields(t *testing.T) {
	tr := tdjsontest.New()
	tr.OnSend = tr.DefaultSend
	e := engine(t, tr)
	c, _ := e.NewClient(nil)
	for _, method := range []string{"sendMessage", "deleteMessages", "forwardMessages", "joinChat", "setOption", "logOut", "viewMessages"} {
		if _, err := c.Call(context.Background(), method, nil); !errors.Is(err, tdjson.ErrReadOnly) {
			t.Fatalf("allowed %s", method)
		}
	}
	if _, err := c.Call(context.Background(), "getMe", map[string]any{"@extra": "injected"}); err == nil {
		t.Fatal("accepted reserved field")
	}
}
func TestErrorRedactionAndFloodWait(t *testing.T) {
	tr := tdjsontest.New()
	tr.OnSend = func(id int, r map[string]any) {
		if r["@type"] == "close" {
			tr.DefaultSend(id, r)
			return
		}
		tr.Reply(id, r, map[string]any{"@type": "error", "code": 429, "message": "Too Many Requests: retry after 45 SECRET_PHONE"})
	}
	e := engine(t, tr)
	c, _ := e.NewClient(nil)
	_, err := c.Call(context.Background(), "getMe", nil)
	var upstream *tdjson.Error
	if !errors.As(err, &upstream) || upstream.RetryAfter != 45*time.Second {
		t.Fatal(err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatal("upstream credential leaked")
	}
}
func TestBoundedPendingRequests(t *testing.T) {
	tr := tdjsontest.New()
	sent := make(chan struct{}, 64)
	tr.OnSend = func(id int, r map[string]any) {
		if r["@type"] == "close" {
			tr.DefaultSend(id, r)
		} else {
			sent <- struct{}{}
		}
	}
	e := engine(t, tr)
	c, _ := e.NewClient(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.Call(ctx, "getMe", nil) }()
	}
	for i := 0; i < 64; i++ {
		<-sent
	}
	if _, err := c.Call(ctx, "getMe", nil); !errors.Is(err, tdjson.ErrOverloaded) {
		t.Fatal(err)
	}
	cancel()
	wg.Wait()
}
func TestCloseWaitsForNativeClosed(t *testing.T) {
	tr := tdjsontest.New()
	closeSent := make(chan struct{})
	tr.OnSend = func(id int, r map[string]any) {
		if r["@type"] == "close" {
			close(closeSent)
		}
	}
	e, err := tdjson.New(tr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = e.NewClient(nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Close(ctx) }()
	<-closeSent
	select {
	case <-done:
		t.Fatal("closed before native acknowledgement")
	case <-time.After(20 * time.Millisecond):
	}
	tr.State(1, "authorizationStateClosed")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestClientCloseRequiresNativeClosed(t *testing.T) {
	tr := tdjsontest.New()
	tr.OnSend = tr.DefaultSend
	e := engine(t, tr)
	c, err := e.NewClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Close(ctx); err != nil {
		t.Fatal("clean native close reported as failure", err)
	}
	if _, err := c.Call(ctx, "getMe", nil); !errors.Is(err, tdjson.ErrClosed) {
		t.Fatal("closed client accepted request", err)
	}
}
