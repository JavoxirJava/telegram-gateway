package httpserver

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
)

type demandChatCursor struct {
	Version  int    `json:"v"`
	Account  string `json:"a"`
	Position string `json:"p"`
}

func (s *Server) demandChats(w http.ResponseWriter, r *http.Request, p access.Principal, limit int) {
	position := ""
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var c demandChatCursor
		body, err := base64.RawURLEncoding.DecodeString(raw)
		if len(raw) > 1024 || err != nil || json.Unmarshal(body, &c) != nil || c.Version != 1 || c.Account != p.AccountID || c.Position == "" {
			writeError(w, 400, "invalid cursor; restart list_chats without a cursor")
			return
		}
		if _, _, err := telegram.ParseChatCursor(c.Position); err != nil {
			writeError(w, 400, "invalid cursor")
			return
		}
		position = c.Position
	}
	fresh, ok := s.refreshRead(w, r, p.AccountID, telegram.ReadRequest{Kind: "chats", Cursor: position, Limit: limit})
	if ok {
		s.writeFreshChats(w, r, p, fresh, "API_CHATS_READ")
	}
}

// Only this request's native results are returned, in Telegram's order. Other
// cached chats (including stale search matches) never enter the response.
func (s *Server) writeFreshChats(w http.ResponseWriter, r *http.Request, p access.Principal, fresh telegram.ReadResult, action string) {
	items := []json.RawMessage{}
	if len(fresh.ChatIDs) > 0 {
		rows, err := s.pool.Query(r.Context(), `SELECT to_jsonb(t)-ARRAY['deleted','deleted_at','photo_object_key']
		 FROM active_chats t WHERE account_id=$1::uuid AND id=ANY($2::uuid[])
		 ORDER BY array_position($2::uuid[],id)`, p.AccountID, fresh.ChatIDs)
		if err != nil {
			writeError(w, 500, "chat lookup failed")
			return
		}
		defer rows.Close()
		for rows.Next() {
			var body []byte
			if err := rows.Scan(&body); err != nil {
				writeError(w, 500, "chat lookup failed")
				return
			}
			items = append(items, json.RawMessage(body))
		}
		if rows.Err() != nil {
			writeError(w, 500, "chat lookup failed")
			return
		}
	}
	next := ""
	if fresh.NextCursor != "" {
		body, _ := json.Marshal(demandChatCursor{Version: 1, Account: p.AccountID, Position: fresh.NextCursor})
		next = base64.RawURLEncoding.EncodeToString(body)
	}
	if !s.auditRead(w, r, p, action, "chat_collection", "", map[string]any{"count": len(items), "has_more": next != ""}) {
		return
	}
	writeJSON(w, 200, map[string]any{"data": items, "count": len(items), "next_cursor": next})
}
