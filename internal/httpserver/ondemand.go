package httpserver

import (
	"errors"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func (s *Server) refreshRead(w http.ResponseWriter, r *http.Request, account string, request telegram.ReadRequest) (telegram.ReadResult, bool) {
	if s.readSync == nil {
		return telegram.ReadResult{}, true
	}
	result, err := s.readSync.Refresh(r.Context(), account, request)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "resource not found")
			return result, false
		}
		s.logger.Warn("on-demand Telegram read failed", "kind", request.Kind, "error", err)
		writeError(w, 503, "Telegram refresh unavailable; retry shortly")
		return result, false
	}
	w.Header().Set("X-Telegram-Sync", "on-demand")
	return result, true
}
