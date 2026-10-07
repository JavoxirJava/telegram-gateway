package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/ratelimit"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/google/uuid"
)

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireScope(w, r, access.ScopeMessagesSend)
	if !ok {
		return
	}
	var in struct {
		Text      string `json:"text"`
		RequestID string `json:"request_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || strings.TrimSpace(in.Text) == "" || !utf8.ValidString(in.Text) || utf8.RuneCountInString(in.Text) > 4096 {
		writeError(w, 400, "text must contain 1 to 4096 characters")
		return
	}
	if _, err := uuid.Parse(in.RequestID); err != nil {
		writeError(w, 400, "request_id must be a UUID; reuse the same UUID when retrying")
		return
	}
	chat := r.PathValue("chatID")
	var tgChat int64
	if err := s.pool.QueryRow(r.Context(), `SELECT telegram_chat_id FROM active_chats WHERE id=$1::uuid AND account_id=$2::uuid`, chat, p.AccountID).Scan(&tgChat); err != nil {
		writeError(w, 404, "chat unavailable")
		return
	}
	if s.sessions == nil {
		writeError(w, 503, "Telegram unavailable")
		return
	}
	session, err := s.sessions.Get(r.Context(), p.AccountID)
	if err != nil {
		writeError(w, 503, "Telegram unavailable")
		return
	}
	sender, ok := session.(telegram.MessageSender)
	if !ok {
		writeError(w, 503, "sending unavailable")
		return
	}
	if delay, err := s.limiter.AccountCooldown(r.Context(), p.AccountID); err != nil || delay > 0 {
		writeError(w, 429, "Telegram cooldown; retry later with the same request_id")
		return
	}
	sum := sha256.Sum256([]byte(chat + "\x00" + in.Text))
	tag, err := s.pool.Exec(r.Context(), `INSERT INTO message_send_requests(account_id,client_id,request_id,chat_id,body_hash) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5) ON CONFLICT DO NOTHING`, p.AccountID, p.ClientID, in.RequestID, chat, sum[:])
	if err != nil {
		writeError(w, 503, "send request could not be recorded")
		return
	}
	if tag.RowsAffected() == 0 {
		var previous []byte
		var status string
		var id *int64
		err = s.pool.QueryRow(r.Context(), `SELECT body_hash,status,telegram_message_id FROM message_send_requests WHERE account_id=$1::uuid AND client_id=$2::uuid AND request_id=$3::uuid`, p.AccountID, p.ClientID, in.RequestID).Scan(&previous, &status, &id)
		if err != nil {
			writeError(w, 503, "send lookup unavailable")
			return
		}
		if !bytes.Equal(sum[:], previous) {
			writeError(w, 409, "request_id already used for another message")
			return
		}
		if status != "accepted" {
			writeError(w, 409, "delivery is pending or unknown; do not resend with a new request_id")
			return
		}
		writeJSON(w, 200, map[string]any{"status": status, "telegram_message_id": id, "request_id": in.RequestID})
		return
	}
	// Once reserved, this key is never automatically dispatched again, including
	// after process crashes or an ambiguous Telegram timeout.
	limit, err := s.limiter.Allow(r.Context(), "rl:send:"+p.AccountID, ratelimit.Limit{Capacity: 5, RefillPerSecond: 0.1, Cost: 1})
	if err != nil || !limit.Allowed {
		_, _ = s.pool.Exec(r.Context(), `DELETE FROM message_send_requests WHERE account_id=$1::uuid AND client_id=$2::uuid AND request_id=$3::uuid`, p.AccountID, p.ClientID, in.RequestID)
		writeError(w, 429, "send rate limit exceeded; retry later with the same request_id")
		return
	}
	if !s.auditRead(w, r, p, "TELEGRAM_SEND_REQUESTED", "chat", chat, map[string]any{"request_id": in.RequestID}) {
		return
	}
	if !s.allowChat(w, r, p.AccountID, chat, true) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	id, sendErr := sender.SendText(ctx, tgChat, in.Text)
	status := "accepted"
	if sendErr != nil {
		status = "unknown"
		if delay, ok := telegram.AsFloodWait(sendErr); ok {
			_, _ = s.limiter.SetAccountCooldown(r.Context(), p.AccountID, delay)
		}
	}
	save, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_, err = s.pool.Exec(save, `UPDATE message_send_requests SET status=$4,telegram_message_id=$5 WHERE account_id=$1::uuid AND client_id=$2::uuid AND request_id=$3::uuid`, p.AccountID, p.ClientID, in.RequestID, status, id)
	if sendErr != nil || err != nil {
		writeError(w, 502, "delivery unknown; check Telegram and reuse this request_id, never automatically resend")
		return
	}
	writeJSON(w, 202, map[string]any{"status": "accepted", "telegram_message_id": id, "request_id": in.RequestID, "note": "Telegram accepted the message; final delivery is not confirmed."})
}
