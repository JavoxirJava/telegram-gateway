package httpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/health"
)

type Server struct {
	logger  *slog.Logger
	checker *health.Checker
}

func New(logger *slog.Logger, checker *health.Checker) http.Handler {
	s := &Server{logger: logger, checker: checker}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.live)
	mux.HandleFunc("GET /health/ready", s.ready)

	return s.securityHeaders(s.accessLog(mux))
}

func (s *Server) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "up",
		"time":   time.Now().UTC(),
	})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	report := s.checker.Check(r.Context())
	statusCode := http.StatusOK
	if report.Status == health.StatusDown {
		statusCode = http.StatusServiceUnavailable
	}
	writeJSON(w, statusCode, report)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"remote_addr", r.RemoteAddr,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
