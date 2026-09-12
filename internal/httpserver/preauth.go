package httpserver

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/JavoxirJava/telegram-gateway/internal/ratelimit"
	"math"
	"net/http"
	"strconv"
	"time"
)

type AdmissionLimiter interface {
	Allow(context.Context, string, ratelimit.Limit) (ratelimit.Result, error)
}

// PreAuth bounds database/introspection abuse before parsing a bearer token.
// RemoteAddr is the actual peer; spoofable forwarding headers are not trusted.
func PreAuth(l AdmissionLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health/live" || r.URL.Path == "/health/ready" {
			next.ServeHTTP(w, r)
			return
		}
		peer := remoteIP(r.RemoteAddr)
		digest := sha256.Sum256([]byte(peer))
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		result, e := l.Allow(ctx, fmt.Sprintf("rl:admission:%x", digest[:]), ratelimit.Limit{Capacity: 30, RefillPerSecond: 2, Cost: 1})
		cancel()
		if e != nil {
			http.Error(w, "admission service unavailable", 503)
			return
		}
		if !result.Allowed {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(result.RetryAfter.Seconds())))))
			http.Error(w, "rate limit exceeded", 429)
			return
		}
		next.ServeHTTP(w, r)
	})
}
