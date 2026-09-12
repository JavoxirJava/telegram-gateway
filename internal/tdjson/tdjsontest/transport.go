// Package tdjsontest is a deterministic in-memory TDLib test double, never used
// by production commands. It tests routing/lifecycle without a Telegram account.
package tdjsontest

import (
	"encoding/json"
	"sync/atomic"
	"time"
)

type Transport struct {
	Frames            chan []byte
	OnSend            func(int, map[string]any)
	next              atomic.Int32
	Receiving         atomic.Int32
	ConcurrentReceive atomic.Bool
}

func New() *Transport                             { return &Transport{Frames: make(chan []byte, 1024)} }
func (t *Transport) CreateClientID() (int, error) { return int(t.next.Add(1)), nil }
func (t *Transport) Send(id int, raw []byte) error {
	var request map[string]any
	if err := json.Unmarshal(raw, &request); err != nil {
		return err
	}
	if t.OnSend != nil {
		t.OnSend(id, request)
	}
	return nil
}
func (t *Transport) Receive(timeout time.Duration) ([]byte, error) {
	if t.Receiving.Add(1) > 1 {
		t.ConcurrentReceive.Store(true)
	}
	defer t.Receiving.Add(-1)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case raw := <-t.Frames:
		return raw, nil
	case <-timer.C:
		return nil, nil
	}
}
func (t *Transport) Close() error { return nil }
func (t *Transport) Emit(id int, body map[string]any) {
	body["@client_id"] = id
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	t.Frames <- raw
}
func (t *Transport) Reply(id int, request, body map[string]any) {
	body["@extra"] = request["@extra"]
	t.Emit(id, body)
}
func (t *Transport) State(id int, state string) {
	t.Emit(id, map[string]any{"@type": "updateAuthorizationState", "authorization_state": map[string]any{"@type": state}})
}
func (t *Transport) DefaultSend(id int, r map[string]any) {
	if r["@type"] == "close" {
		t.State(id, "authorizationStateClosed")
		return
	}
	t.Reply(id, r, map[string]any{"@type": "ok"})
}
