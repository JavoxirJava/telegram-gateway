package httpserver

import (
	"context"
	"errors"
	"github.com/JavoxirJava/telegram-gateway/internal/ratelimit"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type admissionFake struct {
	allowed bool
	fail    bool
	key     string
}

func (f *admissionFake) Allow(_ context.Context, k string, _ ratelimit.Limit) (ratelimit.Result, error) {
	f.key = k
	if f.fail {
		return ratelimit.Result{}, errors.New("offline")
	}
	return ratelimit.Result{Allowed: f.allowed, RetryAfter: 4 * time.Second}, nil
}
func TestPreAuthDeniesBeforeIntrospection(t *testing.T) {
	for _, failure := range []bool{false, true} {
		f := &admissionFake{fail: failure}
		calls := 0
		h := PreAuth(f, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/mcp", nil))
		want := 429
		if failure {
			want = 503
		}
		if w.Code != want || calls != 0 {
			t.Fatal(w.Code, calls)
		}
	}
}
func TestAdmissionDoesNotTrustForwardedIP(t *testing.T) {
	f := &admissionFake{allowed: true}
	h := PreAuth(f, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	r := httptest.NewRequest("POST", "/mcp", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)
	k := f.key
	r.Header.Set("X-Forwarded-For", "10.1.2.3")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if k != f.key {
		t.Fatal("spoofable key")
	}
}
