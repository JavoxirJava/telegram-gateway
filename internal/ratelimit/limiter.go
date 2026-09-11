package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type Limit struct {
	Capacity        float64
	RefillPerSecond float64
	Cost            float64
}

type Result struct {
	Allowed    bool
	Remaining  float64
	RetryAfter time.Duration
}

type Limiter struct {
	redis *redis.Client
}

func New(client *redis.Client) *Limiter {
	return &Limiter{redis: client}
}

var tokenBucketScript = redis.NewScript(`
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local refill = tonumber(ARGV[2])
local cost = tonumber(ARGV[3])

local now_parts = redis.call('TIME')
local now = tonumber(now_parts[1]) + (tonumber(now_parts[2]) / 1000000)

local values = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(values[1])
local last = tonumber(values[2])

if tokens == nil then tokens = capacity end
if last == nil then last = now end

local elapsed = math.max(0, now - last)
tokens = math.min(capacity, tokens + (elapsed * refill))

local allowed = 0
local retry_after = 0
if tokens >= cost then
  tokens = tokens - cost
  allowed = 1
else
  retry_after = (cost - tokens) / refill
end

redis.call('HSET', key, 'tokens', tokens, 'ts', now)
local ttl = math.max(1, math.ceil((capacity / refill) * 2))
redis.call('EXPIRE', key, ttl)

return {tostring(allowed), tostring(tokens), tostring(retry_after)}
`)

var cooldownScript = redis.NewScript(`
local key = KEYS[1]
local requested_ms = tonumber(ARGV[1])
local current_ms = redis.call('PTTL', key)

if current_ms > requested_ms then
  return current_ms
end

redis.call('SET', key, '1', 'PX', requested_ms)
return requested_ms
`)

func (l *Limiter) Allow(ctx context.Context, key string, limit Limit) (Result, error) {
	if strings.TrimSpace(key) == "" {
		return Result{}, errors.New("rate-limit key is required")
	}
	if limit.Capacity <= 0 {
		return Result{}, errors.New("rate-limit capacity must be positive")
	}
	if limit.RefillPerSecond <= 0 {
		return Result{}, errors.New("rate-limit refill rate must be positive")
	}
	if limit.Cost <= 0 {
		limit.Cost = 1
	}
	if limit.Cost > limit.Capacity {
		return Result{}, errors.New("rate-limit cost cannot exceed capacity")
	}

	value, err := tokenBucketScript.Run(ctx, l.redis, []string{key},
		limit.Capacity,
		limit.RefillPerSecond,
		limit.Cost,
	).Result()
	if err != nil {
		return Result{}, fmt.Errorf("execute token bucket: %w", err)
	}

	items, ok := value.([]interface{})
	if !ok || len(items) != 3 {
		return Result{}, fmt.Errorf("unexpected token bucket response: %T", value)
	}

	allowedNumber, err := parseFloat(items[0])
	if err != nil {
		return Result{}, err
	}
	remaining, err := parseFloat(items[1])
	if err != nil {
		return Result{}, err
	}
	retrySeconds, err := parseFloat(items[2])
	if err != nil {
		return Result{}, err
	}

	return Result{
		Allowed:    allowedNumber == 1,
		Remaining:  remaining,
		RetryAfter: time.Duration(retrySeconds * float64(time.Second)),
	}, nil
}

func (l *Limiter) SetAccountCooldown(ctx context.Context, accountID string, duration time.Duration) (time.Duration, error) {
	if strings.TrimSpace(accountID) == "" {
		return 0, errors.New("account id is required")
	}
	if duration <= 0 {
		return 0, errors.New("cooldown duration must be positive")
	}

	milliseconds := duration.Milliseconds()
	if milliseconds < 1 {
		milliseconds = 1
	}

	value, err := cooldownScript.Run(ctx, l.redis, []string{cooldownKey(accountID)}, milliseconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("set account cooldown: %w", err)
	}
	return time.Duration(value) * time.Millisecond, nil
}

func (l *Limiter) AccountCooldown(ctx context.Context, accountID string) (time.Duration, error) {
	if strings.TrimSpace(accountID) == "" {
		return 0, errors.New("account id is required")
	}

	remaining, err := l.redis.PTTL(ctx, cooldownKey(accountID)).Result()
	if err != nil {
		return 0, fmt.Errorf("read account cooldown: %w", err)
	}
	if remaining < 0 {
		return 0, nil
	}
	return remaining, nil
}

func cooldownKey(accountID string) string {
	return "tg:cooldown:account:" + accountID
}

func parseFloat(value interface{}) (float64, error) {
	var raw string
	switch typed := value.(type) {
	case string:
		raw = typed
	case []byte:
		raw = string(typed)
	default:
		return 0, fmt.Errorf("unexpected rate-limit value type %T", value)
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("parse rate-limit value %q: %w", raw, err)
	}
	return parsed, nil
}
