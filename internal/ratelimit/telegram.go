package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Check cooldown and debit account + category budgets in one Redis operation.
// A rejected category must not consume account tokens, or vice versa.
var telegramBudgetScript = redis.NewScript(`
local ttl = redis.call('PTTL', KEYS[1])
if ttl == -1 then return {'0', '60'} end
if ttl > 0 then return {'0', tostring(ttl / 1000)} end
local now_parts = redis.call('TIME')
local now = tonumber(now_parts[1]) + tonumber(now_parts[2]) / 1000000
local tokens = {}
local delay = 0
for i = 1, 2 do
  local cap = tonumber(ARGV[(i-1)*2+1])
  local rate = tonumber(ARGV[(i-1)*2+2])
  local data = redis.call('HMGET', KEYS[i+1], 'tokens', 'ts')
  local old = tonumber(data[1]) or cap
  local ts = tonumber(data[2]) or now
  tokens[i] = math.min(cap, old + math.max(0, now-ts)*rate)
  if tokens[i] < 1 then delay = math.max(delay, (1-tokens[i])/rate) end
end
local allowed = delay == 0
for i = 1, 2 do
  local cap = tonumber(ARGV[(i-1)*2+1])
  local rate = tonumber(ARGV[(i-1)*2+2])
  if allowed then tokens[i] = tokens[i]-1 end
  redis.call('HSET', KEYS[i+1], 'tokens', tokens[i], 'ts', now)
  redis.call('EXPIRE', KEYS[i+1], math.max(1, math.ceil(2*cap/rate)))
end
if allowed then return {'1', '0'} end
return {'0', tostring(delay)}
`)

func (l *Limiter) AllowTelegram(ctx context.Context, accountID, category string, account, method Limit) (Result, error) {
	if l == nil || l.redis == nil {
		return Result{}, errors.New("Telegram limiter is unavailable")
	}
	if strings.TrimSpace(accountID) == "" {
		return Result{}, errors.New("account id is required")
	}
	switch category {
	case "auth", "history", "media", "metadata":
	default:
		return Result{}, errors.New("unknown Telegram request category")
	}
	for _, v := range []Limit{account, method} {
		if math.IsNaN(v.Capacity) || math.IsInf(v.Capacity, 0) || v.Capacity < 1 || v.Capacity > 1e6 || math.IsNaN(v.RefillPerSecond) || math.IsInf(v.RefillPerSecond, 0) || v.RefillPerSecond <= 0 || v.RefillPerSecond > 1e6 || (v.Cost != 0 && v.Cost != 1) {
			return Result{}, errors.New("invalid Telegram request budget")
		}
	}
	raw, err := telegramBudgetScript.Run(ctx, l.redis, []string{cooldownKey(accountID), TelegramAccountKey(accountID), TelegramMethodKey(accountID, "native:"+category)}, account.Capacity, account.RefillPerSecond, method.Capacity, method.RefillPerSecond).Result()
	if err != nil {
		return Result{}, fmt.Errorf("check Telegram request budgets: %w", err)
	}
	items, ok := raw.([]interface{})
	if !ok || len(items) != 2 {
		return Result{}, errors.New("invalid Telegram budget response")
	}
	allowed, err := parseFloat(items[0])
	if err != nil {
		return Result{}, err
	}
	seconds, err := parseFloat(items[1])
	if err != nil {
		return Result{}, err
	}
	return Result{Allowed: allowed == 1, RetryAfter: time.Duration(math.Ceil(seconds*1000)) * time.Millisecond}, nil
}
