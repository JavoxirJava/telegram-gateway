package httpserver

import (
	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"net/http"
)

func (s *Server) syncStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireScope(w, r, access.ScopeProfileRead)
	if !ok {
		return
	}
	if s.readModel == nil {
		writeError(w, 503, "sync status unavailable")
		return
	}
	data, err := s.readModel.Status(r.Context(), p.AccountID)
	if err != nil {
		writeError(w, 503, "sync status unavailable")
		return
	}
	if !s.auditRead(w, r, p, "API_SYNC_STATUS_READ", "account", p.AccountID, nil) {
		return
	}
	writeJSON(w, 200, map[string]any{"data": data})
}
func (s *Server) listMedia(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireScope(w, r, access.ScopeMediaRead)
	if !ok {
		return
	}
	id := r.PathValue("messageID")
	if !canonicalUUID(id) {
		writeError(w, 400, "invalid message id")
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	if s.readModel == nil {
		writeError(w, 503, "media metadata unavailable")
		return
	}
	data, err := s.readModel.Media(r.Context(), p.AccountID, id, limit)
	if err != nil {
		writeError(w, 503, "media metadata unavailable")
		return
	}
	if !s.auditRead(w, r, p, "API_MEDIA_METADATA_READ", "message", id, map[string]any{"count": len(data)}) {
		return
	}
	writeJSON(w, 200, map[string]any{"data": data, "count": len(data)})
}
