package httpserver

import (
	"net/http"
	"strings"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
)

func (s *Server) listContacts(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireScope(w, r, access.ScopeContactsRead)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}

	items, err := s.contacts.ListActive(r.Context(), principal.AccountID, limit)
	if err != nil {
		s.logger.Error("list contacts failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list contacts")
		return
	}
	if !s.auditRead(w, r, principal, "API_CONTACTS_READ", "contact_collection", "", map[string]any{"count": len(items)}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
}

func (s *Server) searchContacts(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireScope(w, r, access.ScopeContactsRead)
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

	items, err := s.contacts.SearchActive(r.Context(), principal.AccountID, query, limit)
	if err != nil {
		s.logger.Error("search contacts failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to search contacts")
		return
	}
	if !s.auditRead(w, r, principal, "API_CONTACTS_SEARCH", "contact_collection", "", map[string]any{"count": len(items)}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
}

func (s *Server) listChatMembers(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireScope(w, r, access.ScopeMembersRead)
	if !ok {
		return
	}
	chatID := strings.TrimSpace(r.PathValue("chatID"))
	if chatID == "" {
		writeError(w, http.StatusBadRequest, "chat id is required")
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}

	items, err := s.members.ListActive(r.Context(), principal.AccountID, chatID, limit)
	if err != nil {
		s.logger.Error("list chat members failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list chat members")
		return
	}
	if !s.auditRead(w, r, principal, "API_CHAT_MEMBERS_READ", "chat", chatID, map[string]any{"count": len(items)}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "count": len(items)})
}
