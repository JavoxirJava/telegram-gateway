package httpserver

import (
	"errors"
	"net/http"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
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

// Legacy signed URLs are retired. Do not redirect to object storage.
func (s *Server) mediaReadURL(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireScope(w, r, access.ScopeMediaRead); !ok {
		return
	}
	writeError(w, http.StatusGone, "signed media URLs are disabled; use the authenticated /content endpoint")
}
