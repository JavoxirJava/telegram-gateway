// Package webauth authenticates each browser against its own Telegram account.
package webauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/ratelimit"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Backend interface {
	NewLoginSession(context.Context, string) (telegram.LoginSession, error)
	AttachLogin(context.Context, string, telegram.Profile) (string, string, error)
	DiscardLoginStorage(context.Context, string)
}
type Principal struct{ UserID, AccountID, Name, CSRF string }
type attempt struct {
	mu                sync.Mutex
	session           telegram.LoginSession
	storage, ip, next string
	expires           time.Time
	profile           *telegram.Profile
	complete          bool
	issued            bool
	closed            bool
	user, account     string
}
type Service struct {
	pool     *pgxpool.Pool
	backend  Backend
	limiter  *ratelimit.Limiter
	logger   *slog.Logger
	base     string
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	mu       sync.Mutex
	attempts map[string]*attempt
}

func New(pool *pgxpool.Pool, backend Backend, base string, limiter *ratelimit.Limiter, logger *slog.Logger) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{pool: pool, backend: backend, base: strings.TrimRight(base, "/"), limiter: limiter, logger: logger, ctx: ctx, cancel: cancel, done: make(chan struct{}), attempts: map[string]*attempt{}}
	go s.maintain()
	return s
}
func (s *Service) Close() { s.cancel(); <-s.done }
func (s *Service) maintain() {
	defer close(s.done)
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
		case <-tick.C:
		}
		s.mu.Lock()
		var expired []*attempt
		for key, a := range s.attempts {
			if s.ctx.Err() != nil || time.Now().After(a.expires) {
				delete(s.attempts, key)
				expired = append(expired, a)
			}
		}
		s.mu.Unlock()
		for _, a := range expired {
			a.mu.Lock()
			a.closed = true
			if a.session != nil {
				a.session.Close()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			s.backend.DiscardLoginStorage(ctx, a.storage)
			cancel()
			a.mu.Unlock()
		}
		if s.ctx.Err() != nil {
			return
		}
	}
}
func random() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func digest(v string) []byte { h := sha256.Sum256([]byte(v)); return h[:] }
func csrf(v string) string   { return base64.RawURLEncoding.EncodeToString(digest("browser-csrf:" + v)) }
func (s *Service) cookieName(kind string) string {
	if strings.HasPrefix(s.base, "https://") {
		return "__Host-tgw_" + kind
	}
	return "tgw_" + kind
}
func (s *Service) cookie(w http.ResponseWriter, kind, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(kind), Value: value, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.base, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func (s *Service) Authenticate(r *http.Request) (Principal, error) {
	cookie, err := r.Cookie(s.cookieName("session"))
	if err != nil || len(cookie.Value) != 43 {
		return Principal{}, errors.New("sign in required")
	}
	var p Principal
	err = s.pool.QueryRow(r.Context(), `SELECT bs.user_id::text,bs.account_id::text,COALESCE(ta.display_name,'Telegram account') FROM browser_sessions bs JOIN telegram_accounts ta ON ta.id=bs.account_id AND ta.user_id=bs.user_id WHERE bs.token_hash=$1 AND bs.expires_at>NOW() AND ta.status='active'`, digest(cookie.Value)).Scan(&p.UserID, &p.AccountID, &p.Name)
	p.CSRF = csrf(cookie.Value)
	return p, err
}
func (s *Service) CheckCSRF(r *http.Request, p Principal) bool {
	return (r.Header.Get("Origin") == "" || r.Header.Get("Origin") == s.base) && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(p.CSRF)) == 1
}
func (s *Service) IssueCookie(ctx context.Context, w http.ResponseWriter, user, account string) error {
	value := random()
	tag, err := s.pool.Exec(ctx, `INSERT INTO browser_sessions(token_hash,user_id,account_id,expires_at) SELECT $1,user_id,id,NOW()+interval '30 days' FROM telegram_accounts WHERE id=$2::uuid AND user_id=$3::uuid AND status='active'`, digest(value), account, user)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("account is unavailable")
	}
	s.cookie(w, "session", value, 30*24*3600)
	return nil
}
func (s *Service) Logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(s.cookieName("session")); err == nil {
		if _, err = s.pool.Exec(r.Context(), `DELETE FROM browser_sessions WHERE token_hash=$1`, digest(c.Value)); err != nil {
			return err
		}
	}
	s.cookie(w, "session", "", -1)
	return nil
}
func LoginURL(next string) string { return "/login?return_to=" + url.QueryEscape(safeNext(next)) }
func safeNext(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host != "" || u.Scheme != "" || u.Fragment != "" {
		return "/account"
	}
	if u.Path != "/oauth/authorize" && u.Path != "/account" {
		return "/account"
	}
	return u.RequestURI()
}
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", s.loginPage)
	mux.Handle("/auth/login/", s.headers(http.HandlerFunc(s.loginRoute)))
}
func (s *Service) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		next.ServeHTTP(w, r)
	})
}
func response(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, message string) {
	response(w, status, map[string]string{"error": message})
}
func (s *Service) allow(r *http.Request, key string, capacity, rate float64) bool {
	if s.limiter == nil {
		return true
	}
	out, err := s.limiter.Allow(r.Context(), key, ratelimit.Limit{Capacity: capacity, RefillPerSecond: rate, Cost: 1})
	return err == nil && out.Allowed
}
