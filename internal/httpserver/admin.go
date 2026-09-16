package httpserver

import (
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"github.com/skip2/go-qrcode"
	"net/http"
	"strings"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	"github.com/JavoxirJava/telegram-gateway/internal/ratelimit"
)

//go:embed manage.html
var manageHTML string

func (s *Server) manage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src data:; frame-ancestors 'none'; base-uri 'none'")
	_, _ = w.Write([]byte(manageHTML))
}
func (s *Server) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		a, b := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(s.adminToken))
		if !ok || len(s.adminToken) < 32 || subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
			writeError(w, 401, "unauthorized")
			return
		}
		result, err := s.limiter.Allow(r.Context(), "rl:admin", ratelimit.Limit{Capacity: 30, RefillPerSecond: 1, Cost: 1})
		if err != nil || !result.Allowed {
			writeError(w, 429, "try again shortly")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		next.ServeHTTP(w, r)
	})
}
func (s *Server) adminAccounts(w http.ResponseWriter, r *http.Request) {
	items, err := s.manager.AccountList(r.Context())
	if err != nil {
		writeError(w, 500, "account lookup failed")
		return
	}
	for _, item := range items {
		if state, ok := item["authorization"].(map[string]any); ok {
			if link, ok := state["link"].(string); ok {
				if png, err := qrcode.Encode(link, qrcode.Medium, 256); err == nil {
					state["qr_image"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
				}
			}
		}
	}
	writeJSON(w, 200, map[string]any{"configured": s.manager.Configured(), "accounts": items})
}
func (s *Server) adminCreateAccount(w http.ResponseWriter, r *http.Request) {
	if !s.manager.Configured() {
		writeError(w, 409, "set TELEGRAM_API_ID, TELEGRAM_API_HASH and TELEGRAM_SESSION_KEY first")
		return
	}
	item, err := s.manager.CreateAccount(r.Context())
	if err != nil {
		writeError(w, 500, "account creation failed")
		return
	}
	writeJSON(w, 201, item)
}
func (s *Server) adminAuth(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("accountID")
	if s.manager.CheckOwner(r.Context(), id) != nil {
		writeError(w, 404, "account not found")
		return
	}
	var in struct {
		Action string `json:"action"`
		Value  string `json:"value"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeError(w, 400, "invalid request")
		return
	}
	if err := s.manager.Authorize(r.Context(), id, in.Action, in.Value); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"status": "accepted"})
}
func (s *Server) adminSync(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("accountID")
	if s.manager.CheckOwner(r.Context(), id) != nil {
		writeError(w, 404, "account not found")
		return
	}
	if s.manager.Sync(r.Context(), id) != nil {
		writeError(w, 409, "account not ready")
		return
	}
	writeJSON(w, 202, map[string]any{"status": "queued"})
}
func (s *Server) adminTokenIssue(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AccountID string         `json:"account_id"`
		Name      string         `json:"name"`
		Scopes    []access.Scope `json:"scopes"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || s.manager.CheckOwner(r.Context(), in.AccountID) != nil {
		writeError(w, 400, "invalid account")
		return
	}
	if len(in.Scopes) == 0 {
		in.Scopes = []access.Scope{access.ScopeProfileRead, access.ScopeChatsList, access.ScopeChatRead, access.ScopeMessagesRead, access.ScopeMessagesSearch, access.ScopeContactsRead, access.ScopeMembersRead, access.ScopeMediaRead}
	}
	if access.ValidateScopes(in.Scopes) != nil {
		writeError(w, 400, "invalid scopes")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = "Personal client"
	}
	clientID, err := s.access.CreateClient(r.Context(), s.manager.OwnerID(), in.Name, "MCP")
	if err != nil {
		writeError(w, 500, "client creation failed")
		return
	}
	expiry := time.Now().Add(30 * 24 * time.Hour)
	token, err := s.access.IssueToken(r.Context(), clientID, in.AccountID, in.Scopes, &expiry)
	if err != nil {
		writeError(w, 409, "account must be active")
		return
	}
	if err := s.audit.Write(r.Context(), audit.Event{AccountID: &in.AccountID, ActorType: audit.ActorAdmin, ActorID: s.manager.OwnerID(), Action: "ACCESS_TOKEN_ISSUED", ResourceType: "gateway_client", ResourceID: clientID}); err != nil {
		writeError(w, 503, "audit unavailable")
		return
	}
	writeJSON(w, 201, map[string]any{"token": token.Plaintext, "client_id": clientID, "expires_at": expiry})
}
func (s *Server) adminClients(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `SELECT id::text,name,status FROM gateway_clients WHERE user_id=$1::uuid ORDER BY created_at DESC`, s.manager.OwnerID())
	if err != nil {
		writeError(w, 500, "client lookup failed")
		return
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var id, name, status string
		if rows.Scan(&id, &name, &status) == nil {
			out = append(out, map[string]string{"id": id, "name": name, "status": status})
		}
	}
	writeJSON(w, 200, out)
}
func (s *Server) adminRevokeClient(w http.ResponseWriter, r *http.Request) {
	_, err := s.pool.Exec(r.Context(), `UPDATE gateway_clients SET status='disabled' WHERE id=$1::uuid AND user_id=$2::uuid`, r.PathValue("clientID"), s.manager.OwnerID())
	if err != nil {
		writeError(w, 500, "revocation failed")
		return
	}
	if err := s.audit.Write(r.Context(), audit.Event{ActorType: audit.ActorAdmin, ActorID: s.manager.OwnerID(), Action: "CLIENT_REVOKED", ResourceType: "gateway_client", ResourceID: r.PathValue("clientID")}); err != nil {
		writeError(w, 503, "audit unavailable")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "revoked"})
}
