package tglive

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

// Gate buffers bounded updates until getMe has verified the Telegram account
// identity. A wrong login must never populate another account's mirror.
type Gate struct {
	mu      sync.Mutex
	opened  bool
	failed  bool
	pending []json.RawMessage
	bytes   int
	handler func(context.Context, json.RawMessage) error
}

func NewGate(handler func(context.Context, json.RawMessage) error) *Gate {
	return &Gate{handler: handler}
}
func (g *Gate) Handle(ctx context.Context, raw json.RawMessage) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failed {
		return errors.New("identity gate failed")
	}
	if !g.opened {
		if g.bytes+len(raw) > 16<<20 || len(g.pending) >= 10000 {
			g.failed = true
			return errors.New("pre-authentication update buffer exhausted")
		}
		g.pending = append(g.pending, append(json.RawMessage(nil), raw...))
		g.bytes += len(raw)
		return nil
	}
	return g.handler(ctx, raw)
}
func (g *Gate) Open(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failed {
		return errors.New("identity gate failed")
	}
	if g.opened {
		return nil
	}
	for _, raw := range g.pending {
		if err := g.handler(ctx, raw); err != nil {
			g.failed = true
			return err
		}
	}
	for _, raw := range g.pending {
		clear(raw)
	}
	g.pending = nil
	g.bytes = 0
	g.opened = true
	return nil
}
