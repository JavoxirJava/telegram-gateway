package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/chats"
	"github.com/JavoxirJava/telegram-gateway/internal/health"
	"github.com/JavoxirJava/telegram-gateway/internal/httpserver"
	"github.com/JavoxirJava/telegram-gateway/internal/media"
	"github.com/JavoxirJava/telegram-gateway/internal/messages"
	"github.com/google/uuid"
)

type sendSession struct {
	historySession
	calls atomic.Int64
}

func (s *sendSession) SendText(_ context.Context, chat int64, text string) (int64, error) {
	s.calls.Add(1)
	if chat != 812 || text != "authorized test" {
		return 0, io.ErrUnexpectedEOF
	}
	return 99, nil
}
func TestChatPermissionsMediaAndSend(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	session := &sendSession{}
	f.deps.Sessions = fakeSessions{session}
	handler := httpserver.New(slog.New(slog.NewTextHandler(io.Discard, nil)), health.NewProbes(nil), f.deps)
	t.Cleanup(handler.Close)
	f.server = httptest.NewServer(handler)
	t.Cleanup(f.server.Close)
	chat, err := f.deps.Chats.Upsert(ctx, chats.Chat{AccountID: f.account, TelegramChatID: 812, ChatType: "private", Title: ptr("permission test")})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := f.deps.Messages.Upsert(ctx, messages.Message{AccountID: f.account, ChatID: chat, TelegramMessageID: 1, MessageType: "Photo", Content: ptr("private content"), SentAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	client, err := f.deps.Access.CreateClient(ctx, f.deps.Manager.OwnerID(), "sender", "MCP")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := f.deps.Access.IssueToken(ctx, client, f.account, []access.Scope{access.ScopeMessagesSend, access.ScopeMessagesRead, access.ScopeMediaRead, access.ScopeChatsList, access.ScopeMessagesSearch}, nil)
	if err != nil {
		t.Fatal(err)
	}
	token := tok.Plaintext
	request := func(method, path string, body any, want int) []byte {
		t.Helper()
		status, out := f.request(t, method, path, token, body)
		if status != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, status, want, out)
		}
		return out
	}
	grant := func(read, send bool) {
		t.Helper()
		_, err := f.pool.Exec(ctx, `INSERT INTO chat_permissions(account_id,chat_id,can_read,can_send) VALUES($1::uuid,$2::uuid,$3,$4) ON CONFLICT(account_id,chat_id) DO UPDATE SET can_read=$3,can_send=$4`, f.account, chat, read, send)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Scope alone cannot read or send; no API permission mutation is exposed.
	for _, suffix := range []string{"messages", "members", "media"} {
		request("GET", "/v1/chats/"+chat+"/"+suffix, nil, 403)
	}
	request("GET", "/v1/messages/search?q=private&chat_id="+chat, nil, 403)
	request("GET", "/v1/messages/search?q=private", nil, 400)
	out := request("GET", "/v1/chats", nil, 200)
	if bytes.Contains(out, []byte(chat)) {
		t.Fatal("denied chat listed")
	}
	id := uuid.NewString()
	body := map[string]any{"text": "authorized test", "request_id": id}
	request("POST", "/v1/chats/"+chat+"/messages", body, 403)
	if session.calls.Load() != 0 {
		t.Fatal("denied send reached Telegram")
	}
	grant(false, true)
	request("GET", "/v1/chats/"+chat+"/messages", nil, 403)
	status, _ := f.request(t, "POST", "/v1/chats/"+chat+"/messages", f.token, body)
	if status != 403 {
		t.Fatal("send scope bypass")
	}
	request("POST", "/v1/chats/"+chat+"/messages", body, 202)
	request("POST", "/v1/chats/"+chat+"/messages", body, 200)
	request("POST", "/v1/chats/"+chat+"/messages", map[string]any{"text": "different", "request_id": id}, 409)
	if session.calls.Load() != 1 {
		t.Fatal("message sent more than once")
	}
	grant(true, false)
	request("POST", "/v1/chats/"+chat+"/messages", map[string]any{"text": "authorized test", "request_id": uuid.NewString()}, 403)
	request("GET", "/v1/chats/"+chat+"/messages", nil, 200)
	// Photo previews return actual image bytes; links and previews both revoke.
	var photo bytes.Buffer
	png.Encode(&photo, image.NewRGBA(image.Rect(0, 0, 20, 10)))
	file := int64(7)
	size := int64(photo.Len())
	repo := media.NewRepository(f.pool)
	mid, err := repo.RegisterPending(ctx, msg, "photo", &file, nil, ptr("image/png"), ptr("photo.png"), &size)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.ClaimDownloadRecoverable(ctx, mid, 5); err != nil || !ok {
		t.Fatal(err)
	}
	if err := f.deps.Media.StoreDownload(ctx, f.account, mid, bytes.NewReader(photo.Bytes()), size, "image/png"); err != nil {
		t.Fatal(err)
	}
	out = request("GET", "/v1/media/"+mid+"/inspect", nil, 200)
	if !bytes.Contains(out, []byte("image/jpeg")) {
		t.Fatal("missing image content")
	}
	request("GET", "/v1/media/"+mid+"/inspect?second=NaN", nil, 400)
	out = request("GET", "/v1/media/"+mid+"/url", nil, 200)
	var ticket struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ticket); err != nil {
		t.Fatal(err)
	}
	ticketPath := ticket.Data.URL[strings.Index(ticket.Data.URL, "/media/"):]
	grant(false, false)
	request("GET", ticketPath, nil, http.StatusForbidden)
	request("GET", "/v1/media/"+mid+"/inspect", nil, 403)
	request("GET", "/v1/media/"+mid+"/url", nil, 403)
	if session.calls.Load() != 1 {
		t.Fatal("revoked send reached Telegram")
	}
}
