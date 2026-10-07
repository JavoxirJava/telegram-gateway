package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/google/uuid"
)

// Chat permissions are account-owned and editable only through browser sessions.
// Check before entering any handler that can fetch or return chat content.
func (s *Server) chatAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := principalFromContext(r.Context())
		chat := r.PathValue("chatID")
		if strings.HasPrefix(r.URL.Path, "/v1/chats/") {
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			if len(parts) >= 4 {
				chat = parts[2]
			}
		}
		if r.URL.Path == "/v1/messages/search" {
			chat = r.URL.Query().Get("chat_id")
			if chat == "" {
				writeError(w, 400, "chat_id is required; select an allowed chat first")
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/v1/media/") {
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			if len(parts) >= 3 {
				if !s.allowMedia(w, r, p.AccountID, parts[2]) {
					return
				}
			}
		}
		if chat != "" && !s.allowChat(w, r, p.AccountID, chat, r.Method == http.MethodPost) {
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) allowChat(w http.ResponseWriter, r *http.Request, account, chat string, send bool) bool {
	if _, err := uuid.Parse(chat); err != nil {
		writeError(w, 400, "invalid chat id")
		return false
	}
	var allowed bool
	err := s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM chat_permissions p JOIN active_chats c ON c.id=p.chat_id AND c.account_id=p.account_id WHERE p.account_id=$1::uuid AND p.chat_id=$2::uuid AND CASE WHEN $3 THEN p.can_send ELSE p.can_read END)`, account, chat, send).Scan(&allowed)
	if err != nil {
		writeError(w, 503, "permission service unavailable")
		return false
	}
	if !allowed {
		writeError(w, 403, "chat access denied; grant permission at /account")
		return false
	}
	return true
}
func (s *Server) allowMedia(w http.ResponseWriter, r *http.Request, account, id string) bool {
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, 400, "invalid media id")
		return false
	}
	var chat string
	if err := s.pool.QueryRow(r.Context(), `SELECT chat_id::text FROM active_message_media WHERE account_id=$1::uuid AND id=$2::uuid`, account, id).Scan(&chat); err != nil {
		writeError(w, 404, "media unavailable")
		return false
	}
	return s.allowChat(w, r, account, chat, false)
}
func (s *Server) allowedChats(w http.ResponseWriter, r *http.Request) {
	scope := access.ScopeChatsList
	if r.URL.Path == "/v1/chats/search" {
		scope = access.ScopeChatRead
	}
	p, ok := s.requireScope(w, r, scope)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	var after any
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		if _, err := uuid.Parse(cursor); err != nil {
			writeError(w, 400, "invalid cursor")
			return
		}
		after = cursor
	}
	rows, err := s.pool.Query(r.Context(), `SELECT c.id::text,jsonb_build_object('id',c.id,'title',c.title,'username',c.username,'chat_type',c.chat_type,'can_read',p.can_read,'can_send',p.can_send) FROM active_chats c JOIN chat_permissions p ON p.chat_id=c.id AND p.account_id=c.account_id WHERE c.account_id=$1::uuid AND (p.can_read OR p.can_send) AND ($2::uuid IS NULL OR c.id>$2::uuid) AND ($3='' OR strpos(lower(COALESCE(c.title,'')||' '||COALESCE(c.username,'')),lower($3))>0) ORDER BY c.id LIMIT $4`, p.AccountID, after, strings.TrimSpace(r.URL.Query().Get("q")), limit+1)
	if err != nil {
		writeError(w, 500, "chat lookup failed")
		return
	}
	defer rows.Close()
	items := []json.RawMessage{}
	ids := []string{}
	for rows.Next() {
		var id string
		var body []byte
		if rows.Scan(&id, &body) != nil {
			writeError(w, 500, "chat lookup failed")
			return
		}
		ids = append(ids, id)
		items = append(items, body)
	}
	if rows.Err() != nil {
		writeError(w, 500, "chat lookup failed")
		return
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = ids[limit-1]
	}
	if !s.auditRead(w, r, p, "API_ALLOWED_CHATS_READ", "chat_collection", "", map[string]any{"count": len(items)}) {
		return
	}
	writeJSON(w, 200, map[string]any{"data": items, "count": len(items), "next_cursor": next})
}
func (s *Server) accountChats(w http.ResponseWriter, r *http.Request) {
	p, ok := s.browserPrincipal(w, r)
	if !ok {
		return
	}
	// GET reads only cached metadata. Explicit POST fetches one metadata page.
	next := ""
	if r.Method == http.MethodPost {
		var in struct {
			Cursor string `json:"cursor"`
			Query  string `json:"query"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			writeError(w, 400, "Noto‘g‘ri so‘rov.")
			return
		}
		req := telegram.ReadRequest{Kind: "chats", Cursor: in.Cursor, Limit: 50}
		if strings.TrimSpace(in.Query) != "" {
			req.Kind = "search_chats"
			req.Query = strings.TrimSpace(in.Query)
		}
		result, ok := s.refreshRead(w, r, p.AccountID, req)
		if !ok {
			return
		}
		next = result.NextCursor
	}
	rows, err := s.pool.Query(r.Context(), `SELECT c.id::text,COALESCE(c.title,c.username,'Chat'),c.chat_type,COALESCE(p.can_read,false),COALESCE(p.can_send,false) FROM active_chats c LEFT JOIN chat_permissions p ON p.chat_id=c.id AND p.account_id=c.account_id WHERE c.account_id=$1::uuid ORDER BY c.title NULLS LAST,c.id`, p.AccountID)
	if err != nil {
		writeError(w, 500, "Chatlarni olish imkoni bo‘lmadi.")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, title, kind string
		var read, send bool
		if rows.Scan(&id, &title, &kind, &read, &send) != nil {
			writeError(w, 500, "Chatlarni olish imkoni bo‘lmadi.")
			return
		}
		items = append(items, map[string]any{"id": id, "title": title, "type": kind, "can_read": read, "can_send": send})
	}
	if rows.Err() != nil {
		writeError(w, 500, "Chatlarni olish imkoni bo‘lmadi.")
		return
	}
	writeJSON(w, 200, map[string]any{"data": items, "next_cursor": next})
}
func (s *Server) accountPermission(w http.ResponseWriter, r *http.Request) {
	p, ok := s.browserPrincipal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("chatID")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, 400, "Chat noto‘g‘ri.")
		return
	}
	var in struct {
		Read bool `json:"can_read"`
		Send bool `json:"can_send"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil {
		writeError(w, 400, "Noto‘g‘ri so‘rov.")
		return
	}
	tag, err := s.pool.Exec(r.Context(), `INSERT INTO chat_permissions(account_id,chat_id,can_read,can_send) SELECT account_id,id,$3,$4 FROM active_chats WHERE account_id=$1::uuid AND id=$2::uuid ON CONFLICT(account_id,chat_id) DO UPDATE SET can_read=EXCLUDED.can_read,can_send=EXCLUDED.can_send,updated_at=NOW()`, p.AccountID, id, in.Read, in.Send)
	if err != nil {
		writeError(w, 500, "Ruxsat saqlanmadi.")
		return
	}
	if tag.RowsAffected() != 1 {
		writeError(w, 404, "Chat topilmadi.")
		return
	}
	if !s.auditRead(w, r, access.Principal{AccountID: p.AccountID, ClientID: "browser-user:" + p.UserID}, "CHAT_PERMISSION_CHANGED", "chat", id, map[string]any{"can_read": in.Read, "can_send": in.Send}) {
		return
	}
	writeJSON(w, 200, map[string]any{"can_read": in.Read, "can_send": in.Send})
}
