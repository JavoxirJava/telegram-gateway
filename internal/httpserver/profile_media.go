package httpserver

import (
	"errors"
	"net/http"
	"strings"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/media"
	"github.com/jackc/pgx/v5"
)

func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireScope(w, r, access.ScopeProfileRead)
	if !ok {
		return
	}

	account, err := s.accounts.GetActive(r.Context(), principal.AccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Telegram account not found")
		return
	}
	if err != nil {
		s.logger.Error("read Telegram profile failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to read Telegram profile")
		return
	}

	if !s.auditRead(w, r, principal, "API_PROFILE_READ", "telegram_account", principal.AccountID, nil) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"account_id":       account.ID,
			"telegram_user_id": account.TelegramUserID,
			"display_name":     account.DisplayName,
			"username":         account.Username,
			"status":           account.Status,
			"connected_at":     account.ConnectedAt,
			"last_update_at":   account.LastUpdateAt,
		},
	})
}

func (s *Server) mediaReadURL(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireScope(w, r, access.ScopeMediaRead)
	if !ok {
		return
	}
	mediaID := strings.TrimSpace(r.PathValue("mediaID"))
	if mediaID == "" {
		writeError(w, http.StatusBadRequest, "media id is required")
		return
	}

	result, err := s.media.CreateReadURL(r.Context(), principal.AccountID, mediaID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "media not found")
		return
	case errors.Is(err, media.ErrNotReady):
		writeError(w, http.StatusConflict, "media is not ready")
		return
	case err != nil:
		s.logger.Error("create media read url failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create media read url")
		return
	}

	if !s.auditRead(w, r, principal, "API_MEDIA_URL_CREATED", "media", mediaID, map[string]any{
		"expires_at": result.ExpiresAt,
	}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}
