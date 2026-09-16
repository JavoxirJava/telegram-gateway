package tdlib

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
)

type object = map[string]any
type transport interface {
	Send([]byte)
	Receive() []byte
	Destroy()
}
type rpc struct {
	transport transport
	mu        sync.Mutex
	pending   map[string]chan object
	stop      chan struct{}
	done      chan struct{}
	once      sync.Once
	seq       atomic.Uint64
	update    func(object)
}

func newRPC(t transport, update func(object)) *rpc {
	r := &rpc{transport: t, pending: make(map[string]chan object), stop: make(chan struct{}), done: make(chan struct{}), update: update}
	go r.receive()
	return r
}
func (r *rpc) receive() {
	defer close(r.done)
	defer r.transport.Destroy()
	for {
		select {
		case <-r.stop:
			return
		default:
		}
		raw := r.transport.Receive()
		if len(raw) == 0 {
			continue
		}
		var v object
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&v) != nil {
			continue
		}
		id := str(v["@extra"])
		if id != "" {
			r.mu.Lock()
			ch := r.pending[id]
			delete(r.pending, id)
			r.mu.Unlock()
			if ch != nil {
				ch <- v
			}
		} else if r.update != nil {
			r.update(v)
		}
	}
}
func (r *rpc) call(ctx context.Context, request object) (object, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	id := strconv.FormatUint(r.seq.Add(1), 10)
	request["@extra"] = id
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	ch := make(chan object, 1)
	r.mu.Lock()
	select {
	case <-r.stop:
		r.mu.Unlock()
		return nil, errors.New("TDLib client closed")
	default:
	}
	r.pending[id] = ch
	r.transport.Send(raw)
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, id); r.mu.Unlock() }()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.done:
		return nil, errors.New("TDLib client closed")
	case v := <-ch:
		if str(v["@type"]) == "error" {
			return nil, tdError(v)
		}
		return v, nil
	}
}
func (r *rpc) close() { r.once.Do(func() { r.mu.Lock(); close(r.stop); r.mu.Unlock() }); <-r.done }

type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("TDLib %d: %s", e.Code, e.Message) }
func tdError(v object) error {
	e := &Error{Code: int(num(v["code"])), Message: str(v["message"])}
	if e.Code == 429 {
		var seconds int64
		for _, word := range strings.Fields(e.Message) {
			if n, err := strconv.ParseInt(strings.Trim(word, ".,"), 10, 64); err == nil && n > 0 {
				seconds = n
			}
		}
		if seconds <= 0 {
			seconds = 60
		}
		return &telegram.FloodWaitError{RetryAfter: time.Duration(seconds) * time.Second, Cause: e}
	}
	return e
}
func obj(v any) object {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return object{}
}
func arr(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}
func str(v any) string { s, _ := v.(string); return s }
func num(v any) int64 {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return i
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}
func yes(v any) bool { b, _ := v.(bool); return b }
func username(v object) string {
	for _, s := range arr(obj(v["usernames"])["active_usernames"]) {
		if str(s) != "" {
			return str(s)
		}
	}
	return ""
}
