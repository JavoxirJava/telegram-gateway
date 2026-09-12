package tdlib

import (
	"context"
	"errors"
	"testing"
)

func TestAuthorizationCannotRebindWithoutNewSession(t *testing.T) {
	s := &Session{changed: make(chan struct{})}
	for _, state := range []string{Ready, WaitPhone, Ready} {
		if e := s.setState([]byte(`{"@type":"`+state+`"}`), false); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Read(context.Background(), "getMe", nil); !errors.Is(e, ErrNotReady) {
		t.Fatal("old identity trust was reused", e)
	}
}
