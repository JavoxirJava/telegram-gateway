package tdjson_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/tdjson"
	"github.com/JavoxirJava/telegram-gateway/internal/tdjson/tdjsontest"
)

func TestLateNativeErrorIsObservedAfterCallerCancellation(t *testing.T) {
	tr := tdjsontest.New()
	type sent struct {
		id      int
		request map[string]any
	}
	received := make(chan sent, 1)
	tr.OnSend = func(id int, r map[string]any) {
		if r["@type"] == "getMe" {
			received <- sent{id, r}
			return
		}
		tr.DefaultSend(id, r)
	}
	engine, err := tdjson.New(tr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := engine.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	observed := make(chan error, 1)
	client, err := engine.NewClient(nil, func(ctx context.Context, err error) error {
		if ctx.Err() != nil {
			t.Error("caller cancellation leaked into native observer")
		}
		observed <- err
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := client.Call(ctx, "getMe", nil); done <- err }()
	var req sent
	select {
	case req = <-received:
	case <-time.After(time.Second):
		t.Fatal("request not sent")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	tr.Reply(req.id, req.request, map[string]any{"@type": "error", "code": 429, "message": "FLOOD_WAIT_45 PRIVATE_SECRET"})
	select {
	case err := <-observed:
		var rate *tdjson.Error
		if !errors.As(err, &rate) || rate.RetryAfter != 45*time.Second {
			t.Fatal(err)
		}
		b, _ := json.Marshal(rate)
		if len(b) == 0 || strings.Contains(string(b), "PRIVATE_SECRET") || strings.Contains(err.Error(), "PRIVATE_SECRET") {
			t.Fatal("missing sanitized error")
		}
	case <-time.After(time.Second):
		t.Fatal("late native error was silently discarded")
	}
}

func TestNativeErrorTransformCannotSuppressFailure(t *testing.T) {
	tr := tdjsontest.New()
	tr.OnSend = func(id int, r map[string]any) {
		if r["@type"] == "getMe" {
			tr.Reply(id, r, map[string]any{"@type": "error", "code": 400, "message": "PRIVATE_SECRET"})
			return
		}
		tr.DefaultSend(id, r)
	}
	engine, err := tdjson.New(tr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = engine.Close(ctx)
	}()
	client, err := engine.NewClient(nil, func(context.Context, error) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.Call(ctx, "getMe", nil); err == nil {
		t.Fatal("native failure was suppressed")
	}
}
