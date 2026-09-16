package httpserver

import (
	"encoding/json"
	"github.com/JavoxirJava/telegram-gateway/internal/gateway"
	"github.com/JavoxirJava/telegram-gateway/internal/mcpserver"
	"github.com/JavoxirJava/telegram-gateway/internal/oauth"
	"github.com/JavoxirJava/telegram-gateway/internal/webauth"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
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
)

type Dependencies struct {
	WebAuth    *webauth.Service
	Pool       *pgxpool.Pool
	Manager    *gateway.Manager
	BaseURL    string
	AdminToken string
	Access     *access.Repository
	Accounts   *accounts.Repository
	Audit      *audit.Writer
	Chats      *chats.Repository
	Contacts   *contacts.Repository
	Members    *members.Repository
	Messages   *messages.Repository
	Media      *media.Service
	Limiter    *ratelimit.Limiter
}

type Server struct {
	handler    http.Handler
	webAuth    *webauth.Service
	pool       *pgxpool.Pool
	manager    *gateway.Manager
	baseURL    string
	adminToken string
	logger     *slog.Logger
	checker    *health.Checker
	access     *access.Repository
	accounts   *accounts.Repository
	audit      *audit.Writer
	chats      *chats.Repository
	contacts   *contacts.Repository
	members    *members.Repository
	messages   *messages.Repository
	media      *media.Service
	limiter    *ratelimit.Limiter
}

func New(logger *slog.Logger, checker *health.Checker, deps Dependencies) *Server {
	s := &Server{
		pool: deps.Pool, manager: deps.Manager, baseURL: deps.BaseURL, adminToken: deps.AdminToken,
		logger:   logger,
		checker:  checker,
		access:   deps.Access,
		accounts: deps.Accounts,
		audit:    deps.Audit,
		chats:    deps.Chats,
		contacts: deps.Contacts,
		members:  deps.Members,
		messages: deps.Messages,
		media:    deps.Media,
		limiter:  deps.Limiter,
	}
	s.webAuth = deps.WebAuth
	if s.webAuth == nil && deps.Manager != nil {
		s.webAuth = webauth.New(deps.Pool, deps.Manager, deps.BaseURL, deps.Limiter, logger)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.live)
	mux.HandleFunc("GET /health/ready", s.ready)

	api := http.NewServeMux()
	api.HandleFunc("GET /v1/profile", s.profile)
	api.HandleFunc("GET /v1/chats", func(w http.ResponseWriter, r *http.Request) {
		s.paged(w, r, access.ScopeChatsList, "active_chats", false)
	})
	api.HandleFunc("GET /v1/chats/search", s.searchChats)
	api.HandleFunc("GET /v1/chats/{chatID}/messages", s.listMessages)
	api.HandleFunc("GET /v1/chats/{chatID}/members", func(w http.ResponseWriter, r *http.Request) {
		s.paged(w, r, access.ScopeMembersRead, "active_chat_members", true)
	})
	api.HandleFunc("GET /v1/messages/search", s.searchMessages)
	api.HandleFunc("GET /v1/contacts", func(w http.ResponseWriter, r *http.Request) {
		s.paged(w, r, access.ScopeContactsRead, "active_contacts", false)
	})
	api.HandleFunc("GET /v1/contacts/search", s.searchContacts)
	api.HandleFunc("GET /v1/media/{mediaID}/url", s.mediaTicket)
	api.HandleFunc("GET /v1/chats/{chatID}/media", func(w http.ResponseWriter, r *http.Request) {
		s.paged(w, r, access.ScopeMediaRead, "active_message_media", true)
	})
	protectedAPI := s.authenticate(api)
	mux.Handle("/v1/", protectedAPI)
	mux.HandleFunc("GET /media/{ticket}", s.downloadTicket)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/account", http.StatusSeeOther) })
	mux.HandleFunc("GET /manage", s.manage)
	if deps.Manager != nil {
		admin := http.NewServeMux()
		admin.HandleFunc("GET /admin/accounts", s.adminAccounts)
		admin.HandleFunc("POST /admin/accounts", s.adminCreateAccount)
		admin.HandleFunc("POST /admin/accounts/{accountID}/auth", s.adminAuth)
		admin.HandleFunc("POST /admin/accounts/{accountID}/sync", s.adminSync)
		admin.HandleFunc("POST /admin/accounts/{accountID}/browser-session", s.adminBrowserSession)
		admin.HandleFunc("POST /admin/tokens", s.adminTokenIssue)
		admin.HandleFunc("GET /admin/clients", s.adminClients)
		admin.HandleFunc("POST /admin/clients/{clientID}/revoke", s.adminRevokeClient)
		mux.Handle("/admin/", s.admin(admin))
		s.webAuth.Register(mux)
		s.registerAccount(mux)
		oauth.New(deps.Pool, deps.BaseURL, s.webAuth, deps.Limiter).Register(mux)
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		principal, _ := principalFromContext(r.Context())
		return mcpserver.New(mcpserver.LocalRead(protectedAPI, r.Header.Get("Authorization"), r.RemoteAddr, r.UserAgent()), principal.Scopes)
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 1 << 20, DisableLocalhostProtection: true})
	// This server is intentionally behind the local Cloudflare proxy. Enforce the
	// configured host/origin ourselves instead of trusting forwarded host headers.
	mux.Handle("/mcp", s.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && origin != s.baseURL {
			writeError(w, 403, "origin is not allowed")
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})))

	s.handler = s.securityHeaders(s.accessLog(mux))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }
func (s *Server) Close() {
	if s.webAuth != nil {
		s.webAuth.Close()
	}
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
		public, _ := url.Parse(s.baseURL)
		incoming, err := url.Parse("http://" + r.Host)
		if err != nil || (r.Host != public.Host && incoming.Hostname() != "127.0.0.1" && incoming.Hostname() != "localhost" && incoming.Hostname() != "::1") {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
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
			"path", logPath(r.URL.Path),
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

func logPath(path string) string {
	if strings.HasPrefix(path, "/media/") {
		return "/media/[ticket]"
	}
	return path
}
