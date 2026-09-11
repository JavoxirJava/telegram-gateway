package httpserver

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/media"
	"github.com/jackc/pgx/v5"
)

func (s *Server) mediaContent(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireScope(w, r, access.ScopeMediaRead)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("mediaID"))
	if !canonicalUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid media id")
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	recheck := func(ctx context.Context) error {
		current, err := s.access.AuthenticateBearer(ctx, token)
		if err != nil {
			return err
		}
		if current.AccountID != principal.AccountID || current.ClientID != principal.ClientID || current.UserID != principal.UserID || !access.HasScope(current.Scopes, access.ScopeMediaRead) {
			return access.ErrInvalidBearer
		}
		return nil
	}
	content, err := s.media.Open(r.Context(), principal.AccountID, id, recheck)
	switch {
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, media.ErrAccessChanged):
		writeError(w, http.StatusNotFound, "media not found")
		return
	case errors.Is(err, access.ErrInvalidBearer):
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	case errors.Is(err, media.ErrNotReady):
		writeError(w, http.StatusConflict, "media is not ready")
		return
	case err != nil:
		s.logger.Error("open media failed")
		writeError(w, http.StatusServiceUnavailable, "media unavailable")
		return
	}
	defer content.Reader.Close()
	// No unauthenticated ranges, caching/304s, or redirects can bypass rechecks.
	if r.Header.Get("Range") != "" {
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(content.Size, 10))
		writeError(w, http.StatusRequestedRangeNotSatisfiable, "ranges are not supported")
		return
	}
	if !s.auditRead(w, r, principal, "API_MEDIA_READ", "media", id, map[string]any{"size": content.Size, "method": r.Method}) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": downloadName(content.FileName)}))
	w.Header().Set("Content-Length", strconv.FormatInt(content.Size, 10))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	// An interrupted stream must not be reported as a successful complete body.
	if _, err := io.CopyBuffer(w, content.Reader, make([]byte, media.ReadChunkSize)); err != nil {
		s.logger.Warn("media stream interrupted", "media_id", id)
		panic(http.ErrAbortHandler)
	}
}
func downloadName(value string) string {
	value = path.Base(strings.ReplaceAll(value, "\\", "/"))
	value = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, value)
	if value == "" || value == "." || value == "/" || len(value) > 200 {
		return "download"
	}
	return value
}
func canonicalUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
