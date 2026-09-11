package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/syncstate"
)

const syncLeaseTTL = 2 * time.Minute

func (p *Processor) acquireSyncLease(ctx context.Context, accountID string, chatID *string, syncType string) (syncstate.Lease, error) {
	state, err := p.syncStates.Ensure(ctx, accountID, chatID, syncType)
	if err != nil {
		return syncstate.Lease{}, fmt.Errorf("ensure sync state: %w", err)
	}
	lease, ok, err := p.syncStates.Acquire(ctx, state.ID, p.workerID, syncLeaseTTL)
	if err != nil {
		return syncstate.Lease{}, fmt.Errorf("acquire sync lease: %w", err)
	}
	if !ok {
		return syncstate.Lease{}, RetryAfter(2*time.Second, fmt.Errorf("sync state is owned by another worker"))
	}
	return lease, nil
}

func (p *Processor) failSync(ctx context.Context, lease syncstate.Lease, cause error) error {
	retryAfter := 15 * time.Second
	if delay, ok := RetryDelay(cause); ok {
		retryAfter = delay
	}
	if err := p.syncStates.Fail(ctx, lease, cause, retryAfter); err != nil {
		return fmt.Errorf("%v; persist sync failure: %w", cause, err)
	}
	return cause
}
