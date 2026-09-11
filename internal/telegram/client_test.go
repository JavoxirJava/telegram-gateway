package telegram

import (
	"errors"
	"testing"
	"time"
)

func TestAsFloodWait(t *testing.T) {
	root := errors.New("too many requests")
	err := &FloodWaitError{RetryAfter: 37 * time.Second, Cause: root}

	delay, ok := AsFloodWait(err)
	if !ok {
		t.Fatal("expected flood wait to be detected")
	}
	if delay != 37*time.Second {
		t.Fatalf("delay = %v, want 37s", delay)
	}
	if !errors.Is(err, root) {
		t.Fatal("expected wrapped cause to be preserved")
	}
}

func TestAsFloodWaitRejectsOrdinaryErrors(t *testing.T) {
	if _, ok := AsFloodWait(errors.New("temporary")); ok {
		t.Fatal("ordinary error must not be treated as flood wait")
	}
}
