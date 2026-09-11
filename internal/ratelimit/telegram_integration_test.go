package ratelimit

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func redisBudgetFixture(t *testing.T) (*Limiter, *redis.Client, string) {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR is not set")
	}
	c := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("integration-%s-%d", t.Name(), time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		keys := []string{cooldownKey(id), TelegramAccountKey(id)}
		for _, k := range []string{"auth", "history", "media", "metadata"} {
			keys = append(keys, TelegramMethodKey(id, "native:"+k))
		}
		_ = c.Del(ctx, keys...).Err()
		_ = c.Close()
	})
	return New(c), c, id
}
func TestTelegramBudgetsAtomicConcurrent(t *testing.T) {
	l, _, id := redisBudgetFixture(t)
	budget := Limit{Capacity: 7, RefillPerSecond: 0.0001, Cost: 1}
	var passed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := l.AllowTelegram(context.Background(), id, "history", budget, budget)
			if err != nil {
				t.Error(err)
				return
			}
			if r.Allowed {
				passed.Add(1)
			}
		}()
	}
	wg.Wait()
	if passed.Load() != 7 {
		t.Fatalf("allowed %d, want 7", passed.Load())
	}
}
func TestTelegramRejectedCategoryDoesNotConsumeAccount(t *testing.T) {
	l, _, id := redisBudgetFixture(t)
	account := Limit{Capacity: 2, RefillPerSecond: 0.0001, Cost: 1}
	method := Limit{Capacity: 1, RefillPerSecond: 0.0001, Cost: 1}
	a, err := l.AllowTelegram(context.Background(), id, "history", account, method)
	if err != nil || !a.Allowed {
		t.Fatal(a, err)
	}
	a, err = l.AllowTelegram(context.Background(), id, "history", account, method)
	if err != nil || a.Allowed {
		t.Fatal(a, err)
	}
	a, err = l.AllowTelegram(context.Background(), id, "media", account, method)
	if err != nil || !a.Allowed {
		t.Fatal("category rejection consumed account quota", a, err)
	}
}
func TestTelegramCooldownCannotBeShortened(t *testing.T) {
	l, _, id := redisBudgetFixture(t)
	ctx := context.Background()
	if _, err := l.SetAccountCooldown(ctx, id, time.Minute); err != nil {
		t.Fatal(err)
	}
	wait, err := l.SetAccountCooldown(ctx, id, time.Second)
	if err != nil || wait < 50*time.Second {
		t.Fatal(wait, err)
	}
	budget := Limit{Capacity: 10, RefillPerSecond: 1, Cost: 1}
	a, err := l.AllowTelegram(ctx, id, "metadata", budget, budget)
	if err != nil || a.Allowed || a.RetryAfter < 50*time.Second {
		t.Fatal(a, err)
	}
}
