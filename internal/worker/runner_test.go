package worker

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDisposition(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		action ackAction
		delay  time.Duration
	}{
		{name: "success", err: nil, action: ackSuccess},
		{name: "permanent", err: Permanent(errors.New("bad job")), action: ackTerminate},
		{name: "explicit retry", err: RetryAfter(42*time.Second, errors.New("busy")), action: ackRetry, delay: 42 * time.Second},
		{name: "generic retry", err: errors.New("temporary"), action: ackRetry, delay: genericRetryDelay},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, delay := disposition(tt.err)
			if action != tt.action {
				t.Fatalf("action = %v, want %v", action, tt.action)
			}
			if delay != tt.delay {
				t.Fatalf("delay = %v, want %v", delay, tt.delay)
			}
		})
	}
}

func TestRetryAfterUsesMinimumDelay(t *testing.T) {
	err := RetryAfter(time.Millisecond, errors.New("retry"))
	delay, ok := RetryDelay(err)
	if !ok {
		t.Fatal("expected retry error")
	}
	if delay != time.Second {
		t.Fatalf("delay = %v, want %v", delay, time.Second)
	}
}

func TestTermReasonIsBounded(t *testing.T) {
	reason := termReason(errors.New(strings.Repeat("x", 500)))
	if len(reason) != 240 {
		t.Fatalf("reason length = %d, want 240", len(reason))
	}
}
