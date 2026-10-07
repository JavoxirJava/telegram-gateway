package oauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOAuthReferrerPolicy(t *testing.T) {
	s := &Server{}
	handler := s.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, tc := range []struct{ path, policy string }{
		{"/oauth/authorize", "same-origin"},
		{"/oauth/token", "no-referrer"},
		{"/oauth/register", "no-referrer"},
		{"/oauth/revoke", "no-referrer"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if got := res.Header().Get("Referrer-Policy"); got != tc.policy {
				t.Fatalf("Referrer-Policy = %q, want %q", got, tc.policy)
			}
		})
	}
}
