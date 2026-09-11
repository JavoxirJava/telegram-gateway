package tglive

import (
	"context"
	"encoding/json"
	"testing"
)

func TestIdentityGateWithholdsUpdates(t *testing.T) {
	var seen []string
	g := NewGate(func(_ context.Context, raw json.RawMessage) error { seen = append(seen, string(raw)); return nil })
	_ = g.Handle(context.Background(), json.RawMessage(`{"n":1}`))
	_ = g.Handle(context.Background(), json.RawMessage(`{"n":2}`))
	if len(seen) != 0 {
		t.Fatal("updates escaped before identity verification")
	}
	if err := g.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = g.Handle(context.Background(), json.RawMessage(`{"n":3}`))
	if len(seen) != 3 || seen[0] != `{"n":1}` || seen[2] != `{"n":3}` {
		t.Fatal("order lost")
	}
}
