package httpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/accounts"
	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	"github.com/JavoxirJava/telegram-gateway/internal/chats"
	"github.com/JavoxirJava/telegram-gateway/internal/contacts"
	"github.com/JavoxirJava/telegram-gateway/internal/health"
	"github.com/JavoxirJava/telegram-gateway/internal/media"
	"github.com/JavoxirJava/telegram-gateway/internal/members"
	"github.com/JavoxirJava/telegram-gateway/internal/messages"
	"github.com/JavoxirJava/telegram-gateway/internal/ratelimit"
	"github.com/JavoxirJava/telegram-gateway/internal/readmodel"
)

type BearerAuthenticator interface {
	AuthenticateBearer(context.Context, string) (access.Principal, error)
}
type AuditWriter interface {
	Write(context.Context, audit.Event) error
}
type MediaReader interface {
	Open(context.Context, string, string, func(context.Context) error) (media.Content, error)
}

type Dependencies struct {
	ReadModel *readmodel.Repository
	Access    BearerAuthenticator
	Accounts  *accounts.Repository
	Audit     AuditWriter
	Chats     *chats.Repository
	Contacts  *contacts.Repository
	Members   *members.Repository
	Messages  *messages.Repository
	Media     MediaReader
	Limiter   *ratelimit.Limiter
}

type Server struct {
	readModel *readmodel.Repository
	logger    *slog.Logger
	checker   *health.Checker
	access    BearerAuthenticator
	accounts  *accounts.Repository
	audit     AuditWriter
	chats     *chats.Repository
	contacts  *contacts.Repository
	members   *members.Repository
	messages  *messages.Repository
	media     MediaReader
	limiter   *ratelimit.Limiter
}

func New(logger *slog.Logger, checker *health.Checker, deps Dependencies) http.Handler {
	s := &Server{
		readModel: deps.ReadModel,
		logger:    logger,
		checker:   checker,
		access:    deps.Access,
		accounts:  deps.Accounts,
		audit:     deps.Audit,
		chats:     deps.Chats,
		contacts:  deps.Contacts,
		members:   deps.Members,
		messages:  deps.Messages,
		media:     deps.Media,
		limiter:   deps.Limiter,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.live)
	mux.HandleFunc("GET /health/ready", s.ready)

	api := http.NewServeMux()
	api.HandleFunc("GET /v1/profile", s.profile)
	api.HandleFunc("GET /v1/sync/status", s.syncStatus)
	api.HandleFunc("GET /v1/messages/{messageID}/media", s.listMedia)
	api.HandleFunc("GET /v1/chats", s.listChats)
	api.HandleFunc("GET /v1/chats/search", s.searchChats)
	api.HandleFunc("GET /v1/chats/{chatID}/messages", s.listMessages)
	api.HandleFunc("GET /v1/chats/{chatID}/members", s.listChatMembers)
	api.HandleFunc("GET /v1/messages/search", s.searchMessages)
	api.HandleFunc("GET /v1/contacts", s.listContacts)
	api.HandleFunc("GET /v1/contacts/search", s.searchContacts)
	api.HandleFunc("GET /v1/media/{mediaID}/url", s.mediaReadURL)
	api.HandleFunc("GET /v1/media/{mediaID}/content", s.mediaContent)
	mux.Handle("/v1/", s.authenticate(api))

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
