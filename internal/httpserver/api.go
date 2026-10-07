package httpserver

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	"github.com/JavoxirJava/telegram-gateway/internal/messages"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/google/uuid"
)

func (s *Server) listChats(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireScope(w, r, access.ScopeChatsList)
	if !ok {
		return
	}

	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	items, err := s.chats.ListActive(r.Context(), principal.AccountID, limit)
	if err != nil {
		s.logger.Error("list chats failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list chats")
		return
	}
	if !s.auditRead(w, r, principal, "API_CHATS_READ", "chat_collection", "", map[string]any{"count": len(items)}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
}

func (s *Server) searchChats(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireScope(w, r, access.ScopeChatRead)
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}

	if s.readSync != nil {
		fresh, ok := s.refreshRead(w, r, principal.AccountID, telegram.ReadRequest{Kind: "search_chats", Query: query, Limit: limit})
		if ok {
			s.writeFreshChats(w, r, principal, fresh, "API_CHATS_SEARCH")
		}
		return
	}
	items, err := s.chats.SearchActive(r.Context(), principal.AccountID, query, limit)
	if err != nil {
		s.logger.Error("search chats failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to search chats")
		return
	}
	if !s.auditRead(w, r, principal, "API_CHATS_SEARCH", "chat_collection", "", map[string]any{"count": len(items)}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireScope(w, r, access.ScopeMessagesRead)
	if !ok {
		return
	}
	chatID := strings.TrimSpace(r.PathValue("chatID"))
	if _, err := uuid.Parse(chatID); err != nil {
		writeError(w, http.StatusBadRequest, "chat id is required")
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}

	var cursor *messages.Cursor
	if rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor")); rawCursor != "" {
		decoded, err := messages.DecodeCursor(rawCursor)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		cursor = &decoded
	}

	request := telegram.ReadRequest{Kind: "messages", ChatID: chatID, Limit: limit}
	if cursor != nil {
		request.BeforeMessageID = cursor.TelegramMessageID
	}
	fresh, ok := s.refreshRead(w, r, principal.AccountID, request)
	if !ok {
		return
	}
	var page messages.Page
	var err error
	if s.readSync != nil {
		page.Items, err = s.messages.ListActiveByIDs(r.Context(), principal.AccountID, fresh.MessageIDs)
		if err == nil && len(page.Items) > limit {
			page.Items = page.Items[:limit]
			last := page.Items[len(page.Items)-1]
			page.NextCursor, err = messages.EncodeCursor(messages.Cursor{SentAt: last.SentAt, TelegramMessageID: last.TelegramMessageID})
		}
	} else {
		page, err = s.messages.ListActivePageByChat(r.Context(), principal.AccountID, chatID, cursor, limit)
	}
	if err != nil {
		s.logger.Error("list messages failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list messages")
		return
	}
	if !s.auditRead(w, r, principal, "API_MESSAGES_READ", "chat", chatID, map[string]any{
		"count":       len(page.Items),
		"has_more":    page.NextCursor != "",
		"used_cursor": cursor != nil,
	}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":        page.Items,
		"count":       len(page.Items),
		"next_cursor": page.NextCursor,
	})
}

func (s *Server) searchMessages(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireScope(w, r, access.ScopeMessagesSearch)
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}

	fresh, ok := s.refreshRead(w, r, principal.AccountID, telegram.ReadRequest{Kind: "search_messages", ChatID: r.URL.Query().Get("chat_id"), Query: query, Limit: limit})
	if !ok {
		return
	}
	var items []messages.Message
	var err error
	if s.readSync != nil {
		items, err = s.messages.ListActiveByIDs(r.Context(), principal.AccountID, fresh.MessageIDs)
	} else {
		items, err = s.messages.SearchActiveChat(r.Context(), principal.AccountID, r.URL.Query().Get("chat_id"), query, limit)
	}
	if err != nil {
		s.logger.Error("search messages failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to search messages")
		return
	}
	if !s.auditRead(w, r, principal, "API_MESSAGES_SEARCH", "message_collection", "", map[string]any{"count": len(items)}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
}

func (s *Server) requireScope(w http.ResponseWriter, r *http.Request, scope access.Scope) (access.Principal, bool) {
	principal, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return access.Principal{}, false
	}
	if !access.HasScope(principal.Scopes, scope) {
		writeError(w, http.StatusForbidden, "insufficient scope")
		return access.Principal{}, false
	}
	return principal, true
}

func (s *Server) auditRead(w http.ResponseWriter, r *http.Request, principal access.Principal, action, resourceType, resourceID string, metadata map[string]any) bool {
	actorType := audit.ActorUser
	if principal.ClientType == "MCP" {
		actorType = audit.ActorMCP
	}
	accountID := principal.AccountID

	event := audit.Event{
		AccountID:    &accountID,
		ActorType:    actorType,
		ActorID:      principal.ClientID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		IPAddress:    remoteIP(r.RemoteAddr),
		UserAgent:    r.UserAgent(),
		Metadata:     metadata,
	}
	if err := s.audit.Write(r.Context(), event); err != nil {
		s.logger.Error("audit write failed", "error", err, "action", action)
		writeError(w, http.StatusServiceUnavailable, "audit service unavailable")
		return false
	}
	return true
}

func parseLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 30, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > 100 {
		writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
		return 0, false
	}
	return value, true
}

func remoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return host
	}
	if net.ParseIP(remoteAddr) != nil {
		return remoteAddr
	}
	return ""
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}
