package httpserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/google/uuid"
	"net/http"
	"time"
)

type pageCursor struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

func (s *Server) paged(w http.ResponseWriter, r *http.Request, scope access.Scope, view string, byChat bool) {
	p, ok := s.requireScope(w, r, scope)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	if view == "active_chats" && s.readSync != nil {
		s.demandChats(w, r, p, limit)
		return
	}
	var cursorID any
	var cursorTime any
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		b, err := base64.RawURLEncoding.DecodeString(raw)
		var c pageCursor
		if err != nil || json.Unmarshal(b, &c) != nil || c.At.IsZero() {
			writeError(w, 400, "invalid cursor")
			return
		}
		if _, err := uuid.Parse(c.ID); err != nil {
			writeError(w, 400, "invalid cursor")
			return
		}
		cursorID = c.ID
		cursorTime = c.At
	}
	order := "created_at"
	switch view {
	case "active_chats":
		order = "COALESCE(last_message_at,'1970-01-01'::timestamptz)"
	case "active_contacts", "active_chat_members", "active_message_media":
	default:
		writeError(w, 500, "invalid collection")
		return
	}
	filter := ""
	var chat any
	if byChat {
		chat = r.PathValue("chatID")
		if _, err := uuid.Parse(chat.(string)); err != nil {
			writeError(w, 400, "invalid chat id")
			return
		}
		filter = " AND chat_id=$5::uuid"
	} else {
		filter = " AND $5::uuid IS NULL"
	}
	// View and order are selected from the fixed list above; user values remain parameters.
	kind := map[string]string{"active_chats": "chats", "active_contacts": "contacts", "active_chat_members": "members", "active_message_media": "messages"}[view]
	if _, ok := s.refreshRead(w, r, p.AccountID, telegram.ReadRequest{Kind: kind, ChatID: r.PathValue("chatID"), Limit: limit}); !ok {
		return
	}
	query := fmt.Sprintf(`SELECT id::text,%s,to_jsonb(t)-ARRAY['phone_hash','object_key','telegram_file_id','unique_file_key','sha256','last_error','deleted','deleted_at'] FROM %s t WHERE account_id=$1::uuid AND ($2::uuid IS NULL OR (%s,id)<($3::timestamptz,$2::uuid)) %s ORDER BY %s DESC,id DESC LIMIT $4`, order, view, order, filter, order)
	rows, err := s.pool.Query(r.Context(), query, p.AccountID, cursorID, cursorTime, limit+1, chat)
	if err != nil {
		s.logger.Error("read collection", "error", err)
		writeError(w, 500, "collection lookup failed")
		return
	}
	defer rows.Close()
	items := []json.RawMessage{}
	cursors := []pageCursor{}
	for rows.Next() {
		var c pageCursor
		var body []byte
		if err := rows.Scan(&c.ID, &c.At, &body); err != nil {
			writeError(w, 500, "collection lookup failed")
			return
		}
		items = append(items, json.RawMessage(body))
		cursors = append(cursors, c)
	}
	if rows.Err() != nil {
		writeError(w, 500, "collection lookup failed")
		return
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		b, _ := json.Marshal(cursors[limit-1])
		next = base64.RawURLEncoding.EncodeToString(b)
	}
	if !s.auditRead(w, r, p, "API_COLLECTION_READ", view, r.PathValue("chatID"), map[string]any{"count": len(items)}) {
		return
	}
	writeJSON(w, 200, map[string]any{"data": items, "count": len(items), "next_cursor": next})
}
