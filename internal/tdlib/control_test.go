package tdlib

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestControlRejectsOriginAndUnknownFields(t *testing.T) {
	s, _ := newTestSession(t)
	h := ControlHandler(s)
	for _, tc := range []struct {
		body, origin string
		status       int
	}{
		{`{"value":"+998901234567","extra":true}`, "", 400},
		{`{"value":"+998901234567"} {}`, "", 400},
		{`{"value":"+998901234567"}`, "https://example.invalid", 403},
	} {
		r := httptest.NewRequest(http.MethodPost, "/auth/phone", strings.NewReader(tc.body))
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("status %d, expected %d", w.Code, tc.status)
		}
		if strings.Contains(w.Body.String(), "998901234567") {
			t.Fatal("credential echoed")
		}
	}
}

func TestControlReturnsRateLimitWithoutEchoingCredentials(t *testing.T) {
	s, _ := newTestSession(t)
	s.config.Requests = &denyPolicy{}
	r := httptest.NewRequest("POST", "/auth/phone", strings.NewReader(`{"value":"+998901234567"}`))
	w := httptest.NewRecorder()
	ControlHandler(s).ServeHTTP(w, r)
	if w.Code != 429 || w.Header().Get("Retry-After") != "60" || strings.Contains(w.Body.String(), "998") {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
}
