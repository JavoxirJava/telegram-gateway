package tdlib

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/ratelimit"
	"github.com/JavoxirJava/telegram-gateway/internal/sessionkey"
	"github.com/JavoxirJava/telegram-gateway/internal/tdjson"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
)

var ErrBudgetUnavailable = errors.New("Telegram request governor unavailable")

type RequestPolicy interface {
	Before(context.Context, string) error
	After(context.Context, error) error
}

type RequestThrottled struct{ After time.Duration }

func (*RequestThrottled) Error() string               { return "Telegram request budget or cooldown reached" }
func (e *RequestThrottled) RetryDelay() time.Duration { return e.After }

type RequestBudget interface {
	AllowTelegram(context.Context, string, string, ratelimit.Limit, ratelimit.Limit) (ratelimit.Result, error)
	SetAccountCooldown(context.Context, string, time.Duration) (time.Duration, error)
}

type RedisPolicy struct {
	budget  RequestBudget
	account string
	blocked atomic.Bool
}

func NewRedisPolicy(budget RequestBudget, account string) (*RedisPolicy, error) {
	if budget == nil || !sessionkey.ValidAccountID(account) {
		return nil, errors.New("request budget and canonical account UUID are required")
	}
	return &RedisPolicy{budget: budget, account: account}, nil
}
func (p *RedisPolicy) Before(ctx context.Context, method string) error {
	if p.blocked.Load() {
		return ErrBudgetUnavailable
	}
	policies := ratelimit.DefaultPolicies()
	category := "metadata"
	limit := policies.TelegramAccount
	switch method {
	case "setAuthenticationPhoneNumber", "checkAuthenticationCode", "checkAuthenticationPassword", "setAuthenticationEmailAddress", "checkAuthenticationEmailCode", "requestQrCodeAuthentication":
		category = "auth"
		limit = ratelimit.Limit{Capacity: 1, RefillPerSecond: 5.0 / 60, Cost: 1}
	case "getChatHistory":
		category = "history"
		limit = policies.History
	case "downloadFile", "getFile":
		category = "media"
		limit = policies.Media
	}
	result, err := p.budget.AllowTelegram(ctx, p.account, category, policies.TelegramAccount, limit)
	if err != nil {
		return ErrBudgetUnavailable
	}
	if !result.Allowed {
		delay := result.RetryAfter
		if delay < time.Second {
			delay = time.Second
		}
		return &RequestThrottled{After: delay}
	}
	return nil
}
func (p *RedisPolicy) After(ctx context.Context, err error) error {
	var upstream *tdjson.Error
	if !errors.As(err, &upstream) || upstream.RetryAfter <= 0 {
		return err
	}
	// Even a cancelled caller must not prevent the server cooldown being saved.
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	delay, saveErr := p.budget.SetAccountCooldown(save, p.account, upstream.RetryAfter+time.Second)
	if saveErr != nil {
		p.blocked.Store(true)
		return ErrBudgetUnavailable
	}
	return &telegram.FloodWaitError{RetryAfter: delay, Cause: upstream}
}

// Every auth/read call passes this gate. Local initialization and close are
// deliberately exempt so an unavailable Redis cannot prevent native shutdown.
func (s *Session) governedCall(ctx context.Context, method string, fields map[string]any) (json.RawMessage, error) {
	select {
	case s.requestGate <- struct{}{}:
		defer func() { <-s.requestGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.Done():
		return nil, tdjson.ErrClosed
	}
	if err := s.config.Requests.Before(ctx, method); err != nil {
		return nil, err
	}
	// The native mailbox transforms all errors, including late responses, before
	// delivery. Do not persist the same cooldown again here.
	return s.client.Call(ctx, method, fields)
}
