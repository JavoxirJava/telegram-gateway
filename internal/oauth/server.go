package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	"github.com/JavoxirJava/telegram-gateway/internal/ratelimit"
	"github.com/JavoxirJava/telegram-gateway/internal/requestinfo"
	"github.com/JavoxirJava/telegram-gateway/internal/webauth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	pool    *pgxpool.Pool
	base    string
	auth    *webauth.Service
	limiter *ratelimit.Limiter
}

func New(pool *pgxpool.Pool, base string, auth *webauth.Service, limiter *ratelimit.Limiter) *Server {
	return &Server{pool: pool, base: strings.TrimRight(base, "/"), auth: auth, limiter: limiter}
}
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.metadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.resourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.resourceMetadata)
	mux.Handle("/oauth/", s.middleware(http.HandlerFunc(s.route)))
}
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	switch r.Method + " " + r.URL.Path {
	case "POST /oauth/register":
		s.register(w, r)
	case "GET /oauth/authorize":
		s.authorize(w, r)
	case "POST /oauth/authorize":
		s.consent(w, r)
	case "POST /oauth/token":
		s.token(w, r)
	case "POST /oauth/revoke":
		s.revoke(w, r)
	default:
		http.NotFound(w, r)
	}
}
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		if r.URL.Path != "/oauth/authorize" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		}
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		ip := requestinfo.ClientIP(r)
		if s.limiter != nil {
			res, err := s.limiter.Allow(r.Context(), "rl:oauth:"+ip, ratelimit.Limit{Capacity: 30, RefillPerSecond: 0.5, Cost: 1})
			if err != nil || !res.Allowed {
				fail(w, 429, "temporarily_unavailable")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func allScopes() []string {
	return []string{"profile:read", "chats:list", "chat:read", "messages:read", "messages:search", "contacts:read", "members:read", "media:read"}
}
func (s *Server) metadata(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 200, map[string]any{
		"issuer": s.base, "authorization_endpoint": s.base + "/oauth/authorize", "token_endpoint": s.base + "/oauth/token", "registration_endpoint": s.base + "/oauth/register", "revocation_endpoint": s.base + "/oauth/revoke",
		"response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"}, "scopes_supported": allScopes(), "authorization_response_iss_parameter_supported": true})
}
func (s *Server) resourceMetadata(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	jsonResponse(w, 200, map[string]any{"resource": s.base + "/mcp", "authorization_servers": []string{s.base}, "scopes_supported": allScopes(), "bearer_methods_supported": []string{"header"}, "resource_name": "Telegram Gateway"})
}

type client struct {
	ID        string
	Name      string
	Redirects []string
	Secret    []byte
	Method    string
}

func (s *Server) getClient(ctx context.Context, id string) (client, error) {
	var c client
	err := s.pool.QueryRow(ctx, `SELECT id,name,redirect_uris,secret_hash,auth_method FROM oauth_clients WHERE id=$1`, id).Scan(&c.ID, &c.Name, &c.Redirects, &c.Secret, &c.Method)
	return c, err
}
func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Host == "" || len(raw) > 2048 {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		return host == "127.0.0.1" || host == "[::1]" || host == "::1" || host == "localhost"
	}
	return false
}
func redirectMatches(registered, actual string) bool {
	if registered == actual {
		return true
	}
	a, e1 := url.Parse(registered)
	b, e2 := url.Parse(actual)
	if e1 != nil || e2 != nil {
		return false
	}
	if a.Scheme != "http" || b.Scheme != "http" || a.Hostname() != b.Hostname() {
		return false
	}
	host := a.Hostname()
	if host != "127.0.0.1" && host != "::1" && host != "localhost" {
		return false
	}
	a.Host = a.Hostname()
	b.Host = b.Hostname()
	return a.String() == b.String()
}
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          string   `json:"client_name"`
		Redirects     []string `json:"redirect_uris"`
		Method        string   `json:"token_endpoint_auth_method"`
		GrantTypes    []string `json:"grant_types"`
		ResponseTypes []string `json:"response_types"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.Redirects) < 1 || len(in.Redirects) > 10 || len(in.Name) > 200 {
		fail(w, 400, "invalid_client_metadata")
		return
	}
	for _, u := range in.Redirects {
		if !validRedirect(u) {
			fail(w, 400, "invalid_redirect_uri")
			return
		}
	}
	for _, v := range in.GrantTypes {
		if v != "authorization_code" && v != "refresh_token" {
			fail(w, 400, "invalid_client_metadata")
			return
		}
	}
	for _, v := range in.ResponseTypes {
		if v != "code" {
			fail(w, 400, "invalid_client_metadata")
			return
		}
	}
	if in.Method == "" {
		in.Method = "none"
	}
	if in.Method != "none" && in.Method != "client_secret_post" && in.Method != "client_secret_basic" {
		fail(w, 400, "invalid_client_metadata")
		return
	}
	if in.Name == "" {
		in.Name = "MCP client"
	}
	id := randomToken()
	secret := ""
	var secretHash []byte
	if in.Method != "none" {
		secret = randomToken()
		secretHash = hash(secret)
	}
	var count int
	if err := s.pool.QueryRow(r.Context(), `SELECT count(*) FROM oauth_clients`).Scan(&count); err != nil || count >= 1000 {
		fail(w, 503, "temporarily_unavailable")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `INSERT INTO oauth_clients(id,name,redirect_uris,secret_hash,auth_method) VALUES($1,$2,$3,$4,$5)`, id, in.Name, in.Redirects, secretHash, in.Method); err != nil {
		fail(w, 500, "server_error")
		return
	}
	out := map[string]any{"client_id": id, "client_name": in.Name, "redirect_uris": in.Redirects, "token_endpoint_auth_method": in.Method, "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}, "client_id_issued_at": time.Now().Unix()}
	if secret != "" {
		out["client_secret"] = secret
		out["client_secret_expires_at"] = 0
	}
	jsonResponse(w, 201, out)
}

var challengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var verifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

func (s *Server) validResource(v string) bool { return v == "" || v == s.base+"/mcp" }
func parseScopes(raw string) ([]string, error) {
	if raw == "" {
		return allScopes(), nil
	}
	values := strings.Fields(raw)
	scopes := make([]access.Scope, len(values))
	for i, v := range values {
		scopes[i] = access.Scope(v)
	}
	normalized, err := access.NormalizeScopes(scopes)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, v := range normalized {
		out = append(out, string(v))
	}
	return out, nil
}
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, err := s.getClient(r.Context(), q.Get("client_id"))
	if err != nil {
		fail(w, 400, "invalid_client")
		return
	}
	redirect := q.Get("redirect_uri")
	matched := false
	for _, v := range c.Redirects {
		if redirectMatches(v, redirect) {
			matched = true
		}
	}
	if !matched || !validRedirect(redirect) {
		fail(w, 400, "invalid_redirect_uri")
		return
	}
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || !challengePattern.MatchString(q.Get("code_challenge")) || !s.validResource(q.Get("resource")) || len(q.Get("state")) > 4096 {
		fail(w, 400, "invalid_request")
		return
	}
	scopes, err := parseScopes(q.Get("scope"))
	if err != nil {
		fail(w, 400, "invalid_scope")
		return
	}
	if s.auth == nil {
		fail(w, 503, "temporarily_unavailable")
		return
	}
	principal, err := s.auth.Authenticate(r)
	if err != nil {
		http.Redirect(w, r, webauth.LoginURL(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	id, csrf := randomToken(), randomToken()
	if _, err := s.pool.Exec(r.Context(), `INSERT INTO oauth_requests(id,client_id,redirect_uri,state,challenge,scopes,csrf_hash,expires_at,user_id,account_id) VALUES($1,$2,$3,$4,$5,$6,$7,NOW()+interval '10 minutes',$8::uuid,$9::uuid)`, id, c.ID, redirect, q.Get("state"), q.Get("code_challenge"), scopes, hash(csrf), principal.UserID, principal.AccountID); err != nil {
		fail(w, 500, "server_error")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "tgw_oauth_" + id[:12], Value: csrf, Path: "/oauth/authorize", Secure: strings.HasPrefix(s.base, "https://"), HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = consentTemplate.Execute(w, map[string]any{"ID": id, "CSRF": csrf, "Client": c.Name, "Redirect": redirect, "Scopes": scopes, "AccountID": principal.AccountID, "AccountName": principal.Name})
}

var consentTemplate = template.Must(template.New("consent").Parse(`<!doctype html><html lang="uz"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Telegram Gateway — ruxsat</title><style>body{font:17px system-ui;max-width:600px;margin:8vh auto;padding:24px;background:#f3f6fa;color:#13263a}form{background:white;border-radius:16px;padding:28px}input,select,button{font:inherit;width:100%;box-sizing:border-box;padding:12px;margin:8px 0 20px}button{background:#176c91;color:white;border:0;border-radius:8px}code{overflow-wrap:anywhere}label{display:block}small{color:#41566c}</style><h1>Telegram Gateway</h1><form method="post" action="/oauth/authorize"><h2>{{.Client}} uchun ruxsat</h2><p>Ushbu dastur tanlangan akkauntning quyidagi ma’lumotlarini o‘qiy oladi:</p><ul>{{range .Scopes}}<li><code>{{.}}</code></li>{{end}}</ul><p>Qaytish manzili: <code>{{.Redirect}}</code></p><input type="hidden" name="request_id" value="{{.ID}}"><input type="hidden" name="csrf" value="{{.CSRF}}"><p>Ulangan Telegram akkaunti: <strong>{{.AccountName}}</strong></p><input type="hidden" name="account_id" value="{{.AccountID}}"><button name="decision" value="allow">O‘qishga ruxsat berish</button><button name="decision" value="deny" formnovalidate>Bekor qilish</button><small>Ruxsat faqat yuqorida ko‘rsatilgan o‘z akkauntingizga beriladi. Ulanishlarni <a href="/account">shaxsiy sahifangizda</a> bekor qilishingiz mumkin.</small></form></html>`))

func (s *Server) consent(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		fail(w, 503, "temporarily_unavailable")
		return
	}
	principal, authErr := s.auth.Authenticate(r)
	if authErr != nil {
		fail(w, 401, "login_required")
		return
	}
	if err := r.ParseForm(); err != nil {
		fail(w, 400, "invalid_request")
		return
	}
	id := r.Form.Get("request_id")
	if len(id) < 12 {
		fail(w, 400, "invalid_request")
		return
	}
	var clientID, redirect, state, challenge, boundUser, boundAccount string
	var scopes []string
	var csrfHash []byte
	err := s.pool.QueryRow(r.Context(), `SELECT client_id,redirect_uri,state,challenge,scopes,csrf_hash,user_id::text,account_id::text FROM oauth_requests WHERE id=$1 AND expires_at>NOW()`, id).Scan(&clientID, &redirect, &state, &challenge, &scopes, &csrfHash, &boundUser, &boundAccount)
	cookie, cookieErr := r.Cookie("tgw_oauth_" + id[:12])
	if err != nil || cookieErr != nil || boundUser != principal.UserID || boundAccount != principal.AccountID || !equalHash(cookie.Value, csrfHash) || !equalHash(r.Form.Get("csrf"), csrfHash) {
		fail(w, 400, "invalid_request")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.base {
		fail(w, 403, "access_denied")
		return
	}
	target, _ := url.Parse(redirect)
	q := target.Query()
	q.Set("state", state)
	q.Set("iss", s.base)
	if r.Form.Get("decision") != "allow" {
		_, _ = s.pool.Exec(r.Context(), `DELETE FROM oauth_requests WHERE id=$1`, id)
		q.Set("error", "access_denied")
		target.RawQuery = q.Encode()
		http.Redirect(w, r, target.String(), 303)
		return
	}
	account := r.Form.Get("account_id")
	if account != principal.AccountID {
		fail(w, 403, "access_denied")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		fail(w, 500, "server_error")
		return
	}
	defer tx.Rollback(context.Background())
	tag, err := tx.Exec(r.Context(), `DELETE FROM oauth_requests WHERE id=$1 AND expires_at>NOW()`, id)
	if err != nil || tag.RowsAffected() != 1 {
		fail(w, 400, "invalid_request")
		return
	}
	var gatewayClient string
	err = tx.QueryRow(r.Context(), `INSERT INTO gateway_clients(user_id,name,client_type) SELECT ta.user_id,oc.name,'MCP' FROM telegram_accounts ta CROSS JOIN oauth_clients oc WHERE ta.id=$1::uuid AND ta.user_id=$2::uuid AND ta.status='active' AND oc.id=$3 RETURNING id::text`, account, principal.UserID, clientID).Scan(&gatewayClient)
	if err != nil {
		fail(w, 400, "access_denied")
		return
	}
	code := randomToken()
	_, err = tx.Exec(r.Context(), `INSERT INTO oauth_codes(code_hash,client_id,gateway_client_id,account_id,redirect_uri,challenge,scopes,expires_at) VALUES($1,$2,$3::uuid,$4::uuid,$5,$6,$7,NOW()+interval '5 minutes')`, hash(code), clientID, gatewayClient, account, redirect, challenge, scopes)
	if err != nil || tx.Commit(r.Context()) != nil {
		fail(w, 500, "server_error")
		return
	}
	if err := audit.NewWriter(s.pool).Write(r.Context(), audit.Event{AccountID: &account, ActorType: audit.ActorSystem, ActorID: "oauth-user:" + principal.UserID, Action: "OAUTH_CONSENT_GRANTED", ResourceType: "gateway_client", ResourceID: gatewayClient, Metadata: map[string]any{"oauth_client_id": clientID, "scopes": scopes}}); err != nil {
		fail(w, 503, "temporarily_unavailable")
		return
	}
	q.Set("code", code)
	target.RawQuery = q.Encode()
	http.Redirect(w, r, target.String(), 303)
}
func (s *Server) authenticateClient(r *http.Request) (client, error) {
	id, secret := r.Form.Get("client_id"), r.Form.Get("client_secret")
	basic := false
	if user, password, ok := r.BasicAuth(); ok {
		id = user
		secret = password
		basic = true
	}
	c, err := s.getClient(r.Context(), id)
	if err != nil {
		return c, errors.New("invalid client")
	}
	switch c.Method {
	case "none":
		if secret != "" || basic {
			return c, errors.New("invalid client")
		}
	case "client_secret_basic":
		if !basic || !equalHash(secret, c.Secret) {
			return c, errors.New("invalid client")
		}
	case "client_secret_post":
		if basic || !equalHash(secret, c.Secret) {
			return c, errors.New("invalid client")
		}
	default:
		return c, errors.New("invalid client")
	}
	return c, nil
}
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !s.validResource(r.Form.Get("resource")) {
		fail(w, 400, "invalid_target")
		return
	}
	c, err := s.authenticateClient(r)
	if err != nil {
		fail(w, 401, "invalid_client")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		fail(w, 500, "server_error")
		return
	}
	defer tx.Rollback(context.Background())
	var account, gatewayClient, redirect, challenge string
	var scopes []string
	// Discover the grant without row locks, then serialize with user revocation.
	// The locked queries below still validate expiry, client ownership and replay.
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		err = tx.QueryRow(r.Context(), `SELECT account_id::text,gateway_client_id::text FROM oauth_codes WHERE code_hash=$1 AND client_id=$2`, hash(r.Form.Get("code")), c.ID).Scan(&account, &gatewayClient)
	case "refresh_token":
		err = tx.QueryRow(r.Context(), `SELECT account_id::text,gateway_client_id::text FROM oauth_refresh_tokens WHERE token_hash=$1 AND client_id=$2`, hash(r.Form.Get("refresh_token")), c.ID).Scan(&account, &gatewayClient)
	default:
		fail(w, 400, "unsupported_grant_type")
		return
	}
	if err != nil {
		fail(w, 400, "invalid_grant")
		return
	}
	if access.LockGrant(r.Context(), tx, account, gatewayClient) != nil {
		fail(w, 503, "temporarily_unavailable")
		return
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		err = tx.QueryRow(r.Context(), `SELECT account_id::text,gateway_client_id::text,redirect_uri,challenge,scopes FROM oauth_codes WHERE code_hash=$1 AND client_id=$2 AND expires_at>NOW() FOR UPDATE`, hash(r.Form.Get("code")), c.ID).Scan(&account, &gatewayClient, &redirect, &challenge, &scopes)
		verifier := r.Form.Get("code_verifier")
		digest := sha256.Sum256([]byte(verifier))
		if err != nil || redirect != r.Form.Get("redirect_uri") || !verifierPattern.MatchString(verifier) || subtle.ConstantTimeCompare([]byte(challenge), []byte(base64.RawURLEncoding.EncodeToString(digest[:]))) != 1 {
			fail(w, 400, "invalid_grant")
			return
		}
		_, err = tx.Exec(r.Context(), `DELETE FROM oauth_codes WHERE code_hash=$1`, hash(r.Form.Get("code")))
	case "refresh_token":
		err = tx.QueryRow(r.Context(), `SELECT account_id::text,gateway_client_id::text,scopes FROM oauth_refresh_tokens WHERE token_hash=$1 AND client_id=$2 AND expires_at>NOW() AND revoked_at IS NULL FOR UPDATE`, hash(r.Form.Get("refresh_token")), c.ID).Scan(&account, &gatewayClient, &scopes)
		if err != nil {
			fail(w, 400, "invalid_grant")
			return
		}
		if requested := r.Form.Get("scope"); requested != "" {
			allowed := map[string]bool{}
			for _, v := range scopes {
				allowed[v] = true
			}
			narrow, parseErr := parseScopes(requested)
			if parseErr != nil {
				fail(w, 400, "invalid_scope")
				return
			}
			for _, v := range narrow {
				if !allowed[v] {
					fail(w, 400, "invalid_scope")
					return
				}
			}
			scopes = narrow
		}
		_, err = tx.Exec(r.Context(), `UPDATE oauth_refresh_tokens SET revoked_at=NOW() WHERE token_hash=$1`, hash(r.Form.Get("refresh_token")))
	default:
		fail(w, 400, "unsupported_grant_type")
		return
	}
	if err != nil {
		fail(w, 500, "server_error")
		return
	}
	token, refresh, err := issue(r.Context(), tx, c.ID, gatewayClient, account, scopes)
	if err != nil {
		fail(w, 400, "invalid_grant")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		fail(w, 500, "server_error")
		return
	}
	if err := audit.NewWriter(s.pool).Write(r.Context(), audit.Event{AccountID: &account, ActorType: audit.ActorSystem, ActorID: "oauth", Action: "OAUTH_TOKEN_ISSUED", ResourceType: "gateway_client", ResourceID: gatewayClient, Metadata: map[string]any{"oauth_client_id": c.ID, "grant_type": r.Form.Get("grant_type"), "scopes": scopes}}); err != nil {
		fail(w, 503, "temporarily_unavailable")
		return
	}
	jsonResponse(w, 200, map[string]any{"access_token": token, "refresh_token": refresh, "token_type": "Bearer", "expires_in": 3600, "scope": strings.Join(scopes, " "), "resource": s.base + "/mcp"})
}
func issue(ctx context.Context, tx pgx.Tx, clientID, gatewayClient, account string, scopes []string) (string, string, error) {
	token, err := access.GenerateToken()
	if err != nil {
		return "", "", err
	}
	refresh := randomToken()
	tag, err := tx.Exec(ctx, `INSERT INTO access_tokens(client_id,account_id,user_id,token_prefix,token_hash,scopes,expires_at) SELECT gc.id,ta.id,gc.user_id,$3,$4,$5,NOW()+interval '1 hour' FROM gateway_clients gc JOIN telegram_accounts ta ON ta.user_id=gc.user_id WHERE gc.id=$1::uuid AND ta.id=$2::uuid AND gc.status='active' AND ta.status='active'`, gatewayClient, account, token.Prefix, token.Hash, scopes)
	if err != nil || tag.RowsAffected() != 1 {
		return "", "", errors.New("account access unavailable")
	}
	_, err = tx.Exec(ctx, `INSERT INTO oauth_refresh_tokens(token_hash,client_id,gateway_client_id,account_id,scopes,expires_at) VALUES($1,$2,$3::uuid,$4::uuid,$5,NOW()+interval '30 days')`, hash(refresh), clientID, gatewayClient, account, scopes)
	return token.Plaintext, refresh, err
}
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	if r.ParseForm() != nil {
		fail(w, 400, "invalid_request")
		return
	}
	c, err := s.authenticateClient(r)
	if err != nil {
		fail(w, 401, "invalid_client")
		return
	}
	tokenHash := hash(r.Form.Get("token"))
	_, err = s.pool.Exec(r.Context(), `UPDATE oauth_refresh_tokens SET revoked_at=NOW() WHERE token_hash=$1 AND client_id=$2`, tokenHash, c.ID)
	if err != nil {
		fail(w, 500, "server_error")
		return
	}
	_, err = s.pool.Exec(r.Context(), `UPDATE access_tokens a SET revoked_at=NOW() WHERE a.token_hash=$1 AND a.client_id IN (SELECT gateway_client_id FROM oauth_refresh_tokens WHERE client_id=$2)`, tokenHash, c.ID)
	if err != nil {
		fail(w, 500, "server_error")
		return
	}
	jsonResponse(w, 200, map[string]any{})
}
func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func hash(v string) []byte { b := sha256.Sum256([]byte(v)); return b[:] }
func equalHash(v string, expected []byte) bool {
	return subtle.ConstantTimeCompare(hash(v), expected) == 1
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code string) {
	jsonResponse(w, status, map[string]string{"error": code})
}
