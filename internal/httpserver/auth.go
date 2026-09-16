package httpserver

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/ratelimit"
)

type principalContextKey struct{}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.baseURL != "" {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.baseURL+`/.well-known/oauth-protected-resource/mcp"`)
		}
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		principal, err := s.access.AuthenticateBearer(r.Context(), token)
		if err != nil {
			if errors.Is(err, access.ErrInvalidBearer) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			s.logger.Error("access token authentication failed", "error", err)
			writeError(w, http.StatusServiceUnavailable, "authentication service unavailable")
			return
		}

		if !s.allowRequest(w, r, principal) {
			return
		}

		w.Header().Del("WWW-Authenticate")
		ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) allowRequest(w http.ResponseWriter, r *http.Request, principal access.Principal) bool {
	policies := ratelimit.DefaultPolicies()
	checks := []struct {
		key   string
		limit ratelimit.Limit
	}{
		{key: ratelimit.MCPClientKey(principal.ClientID), limit: policies.MCPClient},
		{key: ratelimit.UserKey(principal.UserID), limit: policies.User},
	}

	for _, check := range checks {
		result, err := s.limiter.Allow(r.Context(), check.key, check.limit)
		if err != nil {
			s.logger.Error("rate limiter failed", "error", err)
			writeError(w, http.StatusServiceUnavailable, "rate limiter unavailable")
			return false
		}
		if !result.Allowed {
			seconds := int(math.Ceil(result.RetryAfter.Seconds()))
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return false
		}
	}
	return true
}

func principalFromContext(ctx context.Context) (access.Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(access.Principal)
	return principal, ok
}

func bearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return parts[1], true
}
