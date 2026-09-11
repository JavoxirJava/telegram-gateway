package tdlib

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/ratelimit"
	"github.com/JavoxirJava/telegram-gateway/internal/tdjson"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
)

type permitPolicy struct{}

func (permitPolicy) Before(context.Context, string) error     { return nil }
func (permitPolicy) After(_ context.Context, err error) error { return err }

type budgetFake struct {
	allowed       bool
	category      string
	fail          bool
	saveFail      bool
	delay         time.Duration
	saveCancelled bool
}

func (b *budgetFake) AllowTelegram(_ context.Context, _, category string, _, _ ratelimit.Limit) (ratelimit.Result, error) {
	b.category = category
	if b.fail {
		return ratelimit.Result{}, errors.New("redis down")
	}
	return ratelimit.Result{Allowed: b.allowed, RetryAfter: 3 * time.Second}, nil
}
func (b *budgetFake) SetAccountCooldown(ctx context.Context, _ string, d time.Duration) (time.Duration, error) {
	b.delay = d
	b.saveCancelled = ctx.Err() != nil
	if b.saveFail {
		return 0, errors.New("redis down")
	}
	return d, nil
}
func TestNativeBudgetsCategoriesAndFailure(t *testing.T) {
	b := &budgetFake{allowed: true}
	p, err := NewRedisPolicy(b, "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	for method, category := range map[string]string{"getMe": "metadata", "getChatHistory": "history", "downloadFile": "media", "checkAuthenticationPassword": "auth"} {
		if err := p.Before(context.Background(), method); err != nil || b.category != category {
			t.Fatal(method, err, b.category)
		}
	}
	b.allowed = false
	var limited *RequestThrottled
	if err := p.Before(context.Background(), "getMe"); !errors.As(err, &limited) || limited.After != 3*time.Second {
		t.Fatal(err)
	}
	b.fail = true
	if err := p.Before(context.Background(), "getMe"); !errors.Is(err, ErrBudgetUnavailable) {
		t.Fatal(err)
	}
}
func TestNativeFloodWaitPersistsAfterCancellation(t *testing.T) {
	b := &budgetFake{allowed: true}
	p, _ := NewRedisPolicy(b, "11111111-1111-4111-8111-111111111111")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := p.After(ctx, &tdjson.Error{Code: 429, RetryAfter: 45 * time.Second})
	delay, ok := telegram.AsFloodWait(err)
	if !ok || delay != 46*time.Second || b.saveCancelled {
		t.Fatal(err, delay, b.saveCancelled)
	}
	b.saveFail = true
	if err := p.After(ctx, &tdjson.Error{Code: 429, RetryAfter: 45 * time.Second}); !errors.Is(err, ErrBudgetUnavailable) {
		t.Fatal(err)
	}
	b.saveFail = false
	if err := p.Before(context.Background(), "getMe"); !errors.Is(err, ErrBudgetUnavailable) {
		t.Fatal("lost cooldown did not fail closed", err)
	}
}

type denyPolicy struct {
	permitPolicy
	calls int
}

func (p *denyPolicy) Before(context.Context, string) error {
	p.calls++
	return &RequestThrottled{After: time.Minute}
}
func TestSessionCannotBypassNativeGovernor(t *testing.T) {
	s, _ := newTestSession(t)
	p := &denyPolicy{}
	s.config.Requests = p
	if err := s.SubmitPhone(context.Background(), "+998901234567"); err == nil {
		t.Fatal("unlimited auth")
	}
	if err := s.acceptState(json.RawMessage(`{"@type":"authorizationStateReady"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(context.Background(), "getMe", nil); err == nil {
		t.Fatal("unlimited read")
	}
	if p.calls != 2 {
		t.Fatal(p.calls)
	}
}
func TestPolicyValidation(t *testing.T) {
	if _, err := NewRedisPolicy(nil, "bad"); err == nil {
		t.Fatal("invalid policy accepted")
	}
	b := &budgetFake{allowed: true}
	p, _ := NewRedisPolicy(b, "11111111-1111-4111-8111-111111111111")
	upstream := &tdjson.Error{Code: 400}
	if err := p.After(context.Background(), upstream); err != upstream || b.delay != 0 {
		t.Fatal(err)
	}
}
