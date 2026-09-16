package httpserver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"errors"
	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/media"
	"github.com/jackc/pgx/v5"
)

func (s *Server) mediaTicket(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireScope(w, r, access.ScopeMediaRead)
	if !ok {
		return
	}
	id := r.PathValue("mediaID")
	if err := s.media.CheckReady(r.Context(), principal.AccountID, id); err != nil {
		if errors.Is(err, media.ErrNotReady) {
			writeError(w, 409, "media is not ready")
		} else if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "media not found")
		} else {
			writeError(w, 500, "media lookup failed")
		}
		return
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		writeError(w, 500, "could not create link")
		return
	}
	ticket := base64.RawURLEncoding.EncodeToString(raw[:])
	ticketHash := sha256.Sum256([]byte(ticket))
	token, _ := bearerToken(r.Header.Get("Authorization"))
	tokenHash := sha256.Sum256([]byte(token))
	expires := time.Now().Add(5 * time.Minute)
	if !s.auditRead(w, r, principal, "API_MEDIA_URL_CREATED", "media", id, nil) {
		return
	}
	_, err := s.pool.Exec(r.Context(), `INSERT INTO media_read_tickets(ticket_hash,token_hash,media_id,expires_at) VALUES($1,$2,$3::uuid,$4)`, ticketHash[:], tokenHash[:], id, expires)
	if err != nil {
		writeError(w, 500, "could not create link")
		return
	}
	writeJSON(w, 200, map[string]any{"data": map[string]any{"url": s.baseURL + "/media/" + ticket, "expires_at": expires}})
}
func (s *Server) downloadTicket(w http.ResponseWriter, r *http.Request) {
	ticketHash := sha256.Sum256([]byte(r.PathValue("ticket")))
	var accountID, mediaID, clientID string
	err := s.pool.QueryRow(r.Context(), `SELECT at.account_id::text,t.media_id::text,at.client_id::text FROM media_read_tickets t JOIN access_tokens at ON at.token_hash=t.token_hash JOIN gateway_clients gc ON gc.id=at.client_id AND gc.user_id=at.user_id JOIN telegram_accounts ta ON ta.id=at.account_id AND ta.user_id=at.user_id WHERE t.ticket_hash=$1 AND t.expires_at>NOW() AND at.revoked_at IS NULL AND (at.expires_at IS NULL OR at.expires_at>NOW()) AND gc.status='active' AND ta.status='active' AND 'media:read'=ANY(at.scopes)`, ticketHash[:]).Scan(&accountID, &mediaID, &clientID)
	if err != nil {
		writeError(w, 404, "link expired or unavailable")
		return
	}
	reader, _, contentType, name, err := s.media.OpenRead(r.Context(), accountID, mediaID)
	if err != nil {
		writeError(w, 404, "media unavailable")
		return
	}
	defer reader.Close()
	principal := access.Principal{AccountID: accountID, ClientID: clientID, ClientType: "API"}
	if !s.auditRead(w, r, principal, "API_MEDIA_DOWNLOADED", "media", mediaID, nil) {
		return
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Security-Policy", "sandbox")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	_, _ = io.Copy(w, reader)
}
func (s *Server) listChatMedia(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireScope(w, r, access.ScopeMediaRead)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT id::text,message_id::text,media_type,COALESCE(file_name,''),COALESCE(mime_type,''),COALESCE(file_size,0),download_status FROM active_message_media WHERE account_id=$1::uuid AND chat_id=$2::uuid ORDER BY created_at DESC,id DESC LIMIT $3`, p.AccountID, r.PathValue("chatID"), limit)
	if err != nil {
		writeError(w, 500, "media lookup failed")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, messageID, kind, name, mime, status string
		var size int64
		if rows.Scan(&id, &messageID, &kind, &name, &mime, &size, &status) != nil {
			writeError(w, 500, "media lookup failed")
			return
		}
		out = append(out, map[string]any{"id": id, "message_id": messageID, "type": kind, "file_name": name, "mime_type": mime, "size": size, "status": status})
	}
	if !s.auditRead(w, r, p, "API_MEDIA_LIST", "chat", r.PathValue("chatID"), nil) {
		return
	}
	writeJSON(w, 200, map[string]any{"data": out, "count": len(out)})
}
