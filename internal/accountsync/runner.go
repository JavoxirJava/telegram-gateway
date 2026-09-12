package accountsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/objectstore"
	sessionruntime "github.com/JavoxirJava/telegram-gateway/internal/runtime"
	"github.com/JavoxirJava/telegram-gateway/internal/tdadapter"
	"github.com/JavoxirJava/telegram-gateway/internal/tdjson"
	"github.com/JavoxirJava/telegram-gateway/internal/tdlib"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type Native interface {
	telegram.Session
	Message(context.Context, int64, int64) (telegram.Message, error)
	Messages(context.Context, int64, []int64) (map[int64]telegram.Message, []int64, error)
}
type Runner struct {
	queue   *Queue
	native  Native
	objects *objectstore.Store
	log     *slog.Logger
}

func NewRunner(q *Queue, n Native, o *objectstore.Store, log *slog.Logger) (*Runner, error) {
	if q == nil || n == nil || o == nil || log == nil {
		return nil, errors.New("account worker dependencies required")
	}
	return &Runner{queue: q, native: n, objects: o, log: log}, nil
}

const wakeStream = "GATEWAY_ACCOUNT_WAKEUPS"

func wakeSubject(account string) string { return "gateway.wake." + account }

// Run never consumes the legacy multi-account work stream. Database due rows
// are authoritative; JetStream wakeups are latency hints, not delivery claims.
func (r *Runner) Run(ctx context.Context, nc *nats.Conn, js jetstream.JetStream, reconnect <-chan struct{}) error {
	wake := make(chan struct{}, 1)
	if nc == nil || js == nil {
		return errors.New("NATS connection required")
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: wakeStream, Subjects: []string{"gateway.wake.*"}, Storage: jetstream.FileStorage, Retention: jetstream.LimitsPolicy, MaxAge: time.Hour, MaxMsgs: 10000, MaxBytes: 1 << 20, MaxMsgSize: 128, Replicas: 1}); err != nil {
		return err
	}
	sub, err := nc.Subscribe(wakeSubject(r.queue.lease.AccountID), func(_ *nats.Msg) {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()
	// Outside senders may publish account wakeups; native callbacks and polling
	// are sufficient for correctness, including after NATS reconnects.
	if err = r.queue.Start(ctx); err != nil {
		return err
	}
	poll := time.NewTicker(2 * time.Second)
	defer poll.Stop()
	reconcile := time.NewTicker(5 * time.Minute)
	defer reconcile.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Service reconciliation even while the durable queue never becomes empty.
		select {
		case <-reconnect:
			if err := r.queue.Enqueue(ctx, "chats", Payload{Cycle: fmt.Sprintf("reconnect-%d", time.Now().UnixNano())}, false); err != nil {
				return err
			}
		case at := <-reconcile.C:
			if err := r.queue.Enqueue(ctx, "chats", Payload{Cycle: at.UTC().Format("200601021504")}, false); err != nil {
				return err
			}
		default:
		}
		j, found, err := r.queue.Claim(ctx)
		if err != nil {
			return err
		}
		if found {
			callCtx, cancel := context.WithTimeout(ctx, 100*time.Second)
			err = r.perform(callCtx, j)
			cancel()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				if errors.Is(err, sessionruntime.ErrLeaseLost) {
					return err
				}
				delay, code, consume, permanent := retryDisposition(err, j.Attempts)
				if e := r.queue.Retry(ctx, j, delay, code, consume, permanent); e != nil {
					return e
				}
				r.log.Warn("account sync deferred", "kind", j.Kind, "code", code, "retry_after", delay)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-poll.C:
		case <-wake:
		case <-reconnect:
			if err := r.queue.Enqueue(ctx, "chats", Payload{Cycle: fmt.Sprintf("reconnect-%d", time.Now().UnixNano())}, false); err != nil {
				return err
			}
		case at := <-reconcile.C:
			if err := r.queue.Enqueue(ctx, "chats", Payload{Cycle: at.UTC().Format("200601021504")}, false); err != nil {
				return err
			}
		}
	}
}
func retryDisposition(err error, attempts int) (time.Duration, string, bool, bool) {
	var t *tdlib.RequestThrottled
	if errors.As(err, &t) {
		return t.After, "rate_limited", false, false
	}
	if d, ok := telegram.AsFloodWait(err); ok {
		return d, "flood_wait", false, false
	}
	var p *tdadapter.Pending
	if errors.As(err, &p) {
		return p.RetryDelay(), "native_pending", true, false
	}
	if errors.Is(err, tdadapter.ErrCursor) {
		return time.Second, "obsolete_cursor", true, true
	}
	if errors.Is(err, tdadapter.ErrExcluded) {
		return time.Second, "excluded", true, true
	}
	if errors.Is(err, tdadapter.ErrInvalidResponse) || errors.Is(err, tdadapter.ErrFilePath) || errors.Is(err, tdadapter.ErrFileSize) {
		return time.Second, "invalid_native_data", true, true
	}
	var e *tdjson.Error
	if errors.As(err, &e) && (e.Code == 400 || e.Code == 401 || e.Code == 403 || e.Code == 404) {
		return time.Second, fmt.Sprintf("native_%d", e.Code), true, true
	}
	if attempts > 5 {
		attempts = 5
	}
	return time.Duration(1<<attempts) * 15 * time.Second, "dependency_unavailable", true, false
}
func (r *Runner) perform(ctx context.Context, j Job) error {
	switch j.Kind {
	case "chats":
		return r.chats(ctx, j)
	case "history":
		return r.history(ctx, j)
	case "contacts":
		return r.contacts(ctx, j)
	case "members":
		return r.members(ctx, j)
	case "media":
		return r.download(ctx, j)
	case "refresh":
		return r.refresh(ctx, j)
	case "reconcile":
		return r.reconcile(ctx, j)
	}
	return tdadapter.ErrInvalidResponse
}
