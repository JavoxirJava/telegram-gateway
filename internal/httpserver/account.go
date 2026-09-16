package httpserver

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/accounts"
	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	"github.com/JavoxirJava/telegram-gateway/internal/ratelimit"
	"github.com/JavoxirJava/telegram-gateway/internal/webauth"
	"github.com/google/uuid"
)

//go:embed account.html
var accountHTML string

func (s *Server) registerAccount(mux *http.ServeMux) {
	mux.HandleFunc("GET /account", func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.webAuth.Authenticate(r); err != nil {
			http.Redirect(w, r, webauth.LoginURL("/account"), http.StatusSeeOther)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		_, _ = w.Write([]byte(accountHTML))
	})
	mux.HandleFunc("GET /account/data", s.accountData)
	mux.HandleFunc("POST /account/token", s.accountToken)
	mux.HandleFunc("POST /account/sync", s.accountSync)
	mux.HandleFunc("POST /account/logout", s.accountLogout)
	mux.HandleFunc("POST /account/clients/{clientID}/revoke", s.accountRevoke)
}
func (s *Server) browserPrincipal(w http.ResponseWriter, r *http.Request) (webauth.Principal, bool) {
	p, err := s.webAuth.Authenticate(r)
	if err != nil {
		writeError(w, 401, "Telegram akkauntingizga kiring.")
		return p, false
	}
	if r.Method != "GET" && !s.webAuth.CheckCSRF(r, p) {
		writeError(w, 403, "Sahifani yangilab qayta urinib ko‘ring.")
		return p, false
	}
	result, err := s.limiter.Allow(r.Context(), "rl:browser:"+p.AccountID, ratelimit.Limit{Capacity: 30, RefillPerSecond: 1, Cost: 1})
	if err != nil || !result.Allowed {
		writeError(w, 429, "Biroz kutib qayta urinib ko‘ring.")
		return p, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	return p, true
}
func (s *Server) accountData(w http.ResponseWriter, r *http.Request) {
	p, ok := s.browserPrincipal(w, r)
	if !ok {
		return
	}
	var chats, messages, contacts, media int64
	if err := s.pool.QueryRow(r.Context(), `SELECT (SELECT count(*) FROM active_chats WHERE account_id=$1::uuid),(SELECT count(*) FROM active_messages WHERE account_id=$1::uuid),(SELECT count(*) FROM active_contacts WHERE account_id=$1::uuid),(SELECT count(*) FROM active_message_media WHERE account_id=$1::uuid AND download_status='ready')`, p.AccountID).Scan(&chats, &messages, &contacts, &media); err != nil {
		writeError(w, 500, "Ma’lumotlarni olish imkoni bo‘lmadi.")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT gc.id::text,gc.name,MAX(t.created_at),(gc.status='active' AND (BOOL_OR(t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at>NOW())) OR EXISTS(SELECT 1 FROM oauth_refresh_tokens rt WHERE rt.gateway_client_id=gc.id AND rt.account_id=$2::uuid AND rt.revoked_at IS NULL AND rt.expires_at>NOW()))) FROM gateway_clients gc JOIN access_tokens t ON t.client_id=gc.id AND t.user_id=gc.user_id WHERE gc.user_id=$1::uuid AND t.account_id=$2::uuid GROUP BY gc.id,gc.name ORDER BY MAX(t.created_at) DESC LIMIT 100`, p.UserID, p.AccountID)
	if err != nil {
		writeError(w, 500, "Ulanishlarni olish imkoni bo‘lmadi.")
		return
	}
	defer rows.Close()
	clients := []map[string]any{}
	for rows.Next() {
		var id, name string
		var created time.Time
		var active bool
		if rows.Scan(&id, &name, &created, &active) != nil {
			writeError(w, 500, "Ulanishlarni olish imkoni bo‘lmadi.")
			return
		}
		clients = append(clients, map[string]any{"id": id, "name": name, "active": active, "created_at": created})
	}
	if rows.Err() != nil {
		writeError(w, 500, "Ulanishlarni olish imkoni bo‘lmadi.")
		return
	}
	writeJSON(w, 200, map[string]any{"account_id": p.AccountID, "name": p.Name, "csrf": p.CSRF, "authorization": s.manager.AccountState(p.AccountID), "stats": map[string]int64{"chats": chats, "messages": messages, "contacts": contacts, "media": media}, "clients": clients, "mcp_url": s.baseURL + "/mcp"})
}
func (s *Server) accountToken(w http.ResponseWriter, r *http.Request) {
	p, ok := s.browserPrincipal(w, r)
	if !ok {
		return
	}
	var in struct {
		Name   string         `json:"name"`
		Scopes []access.Scope `json:"scopes"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		writeError(w, 400, "Noto‘g‘ri so‘rov.")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		in.Name = "Personal MCP client"
	}
	if len(in.Name) > 120 {
		writeError(w, 400, "Klient nomi juda uzun.")
		return
	}
	if len(in.Scopes) == 0 {
		in.Scopes = []access.Scope{access.ScopeProfileRead, access.ScopeChatsList, access.ScopeChatRead, access.ScopeMessagesRead, access.ScopeMessagesSearch, access.ScopeContactsRead, access.ScopeMembersRead, access.ScopeMediaRead}
	}
	if access.ValidateScopes(in.Scopes) != nil {
		writeError(w, 400, "Noto‘g‘ri ruxsatlar.")
		return
	}
	client, err := s.access.CreateClient(r.Context(), p.UserID, in.Name, "MCP")
	if err != nil {
		writeError(w, 500, "Klient yaratilmadi.")
		return
	}
	expires := time.Now().Add(30 * 24 * time.Hour)
	token, err := s.access.IssueToken(r.Context(), client, p.AccountID, in.Scopes, &expires)
	if err != nil {
		writeError(w, 500, "Token yaratilmadi.")
		return
	}
	if err := s.audit.Write(r.Context(), audit.Event{AccountID: &p.AccountID, ActorType: audit.ActorSystem, ActorID: "browser-user:" + p.UserID, Action: "USER_ACCESS_TOKEN_ISSUED", ResourceType: "gateway_client", ResourceID: client}); err != nil {
		writeError(w, 503, "Token berishni yakunlab bo‘lmadi.")
		return
	}
	writeJSON(w, 201, map[string]any{"token": token.Plaintext, "client_id": client, "expires_at": expires})
}
func (s *Server) accountSync(w http.ResponseWriter, r *http.Request) {
	p, ok := s.browserPrincipal(w, r)
	if !ok {
		return
	}
	limit, err := s.limiter.Allow(r.Context(), "rl:user-sync:"+p.AccountID, ratelimit.Limit{Capacity: 1, RefillPerSecond: 1.0 / 60, Cost: 1})
	if err != nil || !limit.Allowed {
		writeError(w, 429, "Sinxronlash yaqinda so‘ralgan. Bir daqiqa kuting.")
		return
	}
	if s.manager.Sync(r.Context(), p.AccountID) != nil {
		writeError(w, 409, "Telegram ulanishi tayyorlanmoqda.")
		return
	}
	writeJSON(w, 202, map[string]string{"status": "queued"})
}
func (s *Server) accountLogout(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.browserPrincipal(w, r); !ok {
		return
	}
	if s.webAuth.Logout(w, r) != nil {
		writeError(w, 500, "Chiqish amalga oshmadi.")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "signed_out"})
}
func (s *Server) accountRevoke(w http.ResponseWriter, r *http.Request) {
	p, ok := s.browserPrincipal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("clientID")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, 404, "Ulanish topilmadi.")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "Ulanishni bekor qilib bo‘lmadi.")
		return
	}
	defer tx.Rollback(context.Background())
	if access.LockGrant(r.Context(), tx, p.AccountID, id) != nil {
		writeError(w, 503, "Ulanishni bekor qilib bo‘lmadi.")
		return
	}
	var exists bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM access_tokens WHERE client_id=$1::uuid AND account_id=$2::uuid AND user_id=$3::uuid)`, id, p.AccountID, p.UserID).Scan(&exists); err != nil || !exists {
		writeError(w, 404, "Ulanish topilmadi.")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE access_tokens SET revoked_at=COALESCE(revoked_at,NOW()) WHERE client_id=$1::uuid AND account_id=$2::uuid AND user_id=$3::uuid`, id, p.AccountID, p.UserID); err != nil {
		writeError(w, 500, "Ulanishni bekor qilib bo‘lmadi.")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE oauth_refresh_tokens SET revoked_at=COALESCE(revoked_at,NOW()) WHERE gateway_client_id=$1::uuid AND account_id=$2::uuid`, id, p.AccountID); err != nil {
		writeError(w, 500, "Ulanishni bekor qilib bo‘lmadi.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "Ulanishni bekor qilib bo‘lmadi.")
		return
	}
	if err = s.audit.Write(r.Context(), audit.Event{AccountID: &p.AccountID, ActorType: audit.ActorSystem, ActorID: "browser-user:" + p.UserID, Action: "USER_CLIENT_REVOKED", ResourceType: "gateway_client", ResourceID: id}); err != nil {
		writeError(w, 503, "Bekor qilish yozuvini saqlab bo‘lmadi.")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "revoked"})
}
func (s *Server) adminBrowserSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("accountID")
	if s.manager.CheckOwner(r.Context(), id) != nil {
		writeError(w, 404, "account not found")
		return
	}
	a, err := s.accounts.GetActive(r.Context(), id)
	if err != nil || a.Status != accounts.StatusActive {
		writeError(w, 409, "account is not active")
		return
	}
	if s.webAuth.IssueCookie(r.Context(), w, a.UserID, a.ID) != nil {
		writeError(w, 500, "could not open account page")
		return
	}
	writeJSON(w, 200, map[string]string{"redirect": "/account"})
}
