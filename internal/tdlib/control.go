package tdlib

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
)

// ControlHandler must only be bound to a private Unix socket in a 0700
// directory (socket 0600). It is NOT a public auth API, OAuth endpoint or MCP
// tool. No request bodies, codes, passwords or QR links may be logged.
func ControlHandler(s *Session) http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, status int, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
	}
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) { reply(w, http.StatusOK, s.State()) })
	mux.HandleFunc("POST /auth/{step}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Value string `json:"value"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			reply(w, 400, map[string]string{"error": "invalid request"})
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			reply(w, 400, map[string]string{"error": "invalid request"})
			return
		}
		var err error
		switch r.PathValue("step") {
		case "phone":
			err = s.SubmitPhone(r.Context(), body.Value)
		case "code":
			err = s.SubmitCode(r.Context(), body.Value)
		case "password":
			err = s.SubmitPassword(r.Context(), body.Value)
		case "email":
			err = s.SubmitEmail(r.Context(), body.Value)
		case "email-code":
			err = s.SubmitEmailCode(r.Context(), body.Value)
		case "qr":
			err = s.RequestQR(r.Context())
		default:
			reply(w, 404, map[string]string{"error": "unknown authentication step"})
			return
		}
		if err != nil {
			var throttled *RequestThrottled
			delay, flood := telegram.AsFloodWait(err)
			if errors.As(err, &throttled) {
				delay = throttled.After
			}
			if flood || throttled != nil {
				if delay < time.Second {
					delay = time.Second
				}
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(delay.Seconds()))))
				reply(w, 429, map[string]string{"error": "authentication rate limited"})
				return
			}
			if errors.Is(err, ErrBudgetUnavailable) {
				reply(w, 503, map[string]string{"error": "authentication unavailable"})
				return
			}
			reply(w, 400, map[string]string{"error": "authentication operation failed", "state": s.State().Type})
			return
		}
		reply(w, 200, map[string]string{"status": "accepted", "state": s.State().Type})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		// Prevent a browser-origin request via an accidentally installed local proxy.
		if r.Header.Get("Origin") != "" {
			reply(w, 403, map[string]string{"error": "browser-origin requests are not allowed"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}
