package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/health"
	"github.com/JavoxirJava/telegram-gateway/internal/httpserver"
	"github.com/JavoxirJava/telegram-gateway/internal/media"
	"github.com/JavoxirJava/telegram-gateway/internal/syncjob"
	"github.com/JavoxirJava/telegram-gateway/internal/syncstate"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/JavoxirJava/telegram-gateway/internal/worker"
)

type demandSession struct {
	historySession
	chats, history, downloads, searches, chatSearches atomic.Int64
}

func (s *demandSession) ListChats(_ context.Context, cursor string, limit int) (telegram.ChatPage, error) {
	s.chats.Add(1)
	if limit != 2 {
		return telegram.ChatPage{}, fmt.Errorf("limit not forwarded: %d", limit)
	}
	switch cursor {
	case "":
		return telegram.ChatPage{Items: []telegram.Chat{{TelegramChatID: 987, Type: "private", Title: "Requested chat"}}, NextCursor: "0:1"}, nil
	case "0:1":
		return telegram.ChatPage{NextCursor: "1:0"}, nil
	case "1:0":
		return telegram.ChatPage{Items: []telegram.Chat{{TelegramChatID: 988, Type: "private", Title: "Archived chat"}}}, nil
	default:
		return telegram.ChatPage{}, fmt.Errorf("unexpected cursor: %s", cursor)
	}
}
func (s *demandSession) SearchChats(_ context.Context, query string, limit int) ([]telegram.Chat, error) {
	s.chatSearches.Add(1)
	if query != "Requested" || limit != 2 {
		return nil, fmt.Errorf("search parameters not forwarded")
	}
	return []telegram.Chat{{TelegramChatID: 777, Type: "supergroup", Title: "Requested server match"}}, nil
}
func (s *demandSession) GetChatHistory(_ context.Context, chat, before int64, limit int) ([]telegram.Message, error) {
	s.history.Add(1)
	items := []telegram.Message{}
	for id := int64(5); id > 0 && len(items) < limit; id-- {
		if before > 0 && id >= before {
			continue
		}
		items = append(items, telegram.Message{TelegramMessageID: id, Type: "Document", Content: ptr("demand match"), SentAt: time.Unix(1700000000+id, 0), Media: []telegram.Media{{Type: "document", TelegramFileID: 42, UniqueFileKey: "attachment"}}})
	}
	return items, nil
}
func (s *demandSession) DownloadFile(context.Context, int64) (telegram.Download, error) {
	s.downloads.Add(1)
	return telegram.Download{Reader: io.NopCloser(strings.NewReader("test")), Size: 4, ContentType: "text/plain"}, nil
}
func (s *demandSession) SearchMessages(context.Context, string, int) ([]telegram.LocatedMessage, error) {
	s.searches.Add(1)
	return []telegram.LocatedMessage{{Chat: telegram.Chat{TelegramChatID: 987, Type: "private"}, Message: telegram.Message{TelegramMessageID: 99, Type: "Text", Content: ptr("demand match"), SentAt: time.Unix(1700000099, 0)}}}, nil
}

type demandPublisher struct {
	fakePublisher
	calls atomic.Int64
}

func (p *demandPublisher) EnqueueChatHistory(context.Context, string, syncjob.ChatHistoryPayload) error {
	p.calls.Add(1)
	return nil
}
func (p *demandPublisher) EnqueueMediaDownload(context.Context, string, syncjob.MediaDownloadPayload) error {
	p.calls.Add(1)
	return nil
}
func (p *demandPublisher) EnqueueChatMembers(context.Context, string, syncjob.ChatMembersPayload) error {
	p.calls.Add(1)
	return nil
}
func (p *demandPublisher) EnqueueContactsSync(context.Context, string) error {
	p.calls.Add(1)
	return nil
}
func (p *demandPublisher) EnqueueAccountBootstrapPage(context.Context, string, string) error {
	p.calls.Add(1)
	return nil
}

func TestOnDemandReadsDoNotFanOut(t *testing.T) {
	f := setup(t)
	session := &demandSession{}
	publisher := &demandPublisher{}
	p, err := worker.NewProcessor(worker.Dependencies{WorkerID: "demand-test", Sessions: fakeSessions{session}, Accounts: f.deps.Accounts, Chats: f.deps.Chats, Messages: f.deps.Messages, Contacts: f.deps.Contacts, Members: f.deps.Members, MediaRepo: media.NewRepository(f.pool), Media: f.deps.Media, SyncStates: syncstate.NewRepository(f.pool), Publisher: publisher, Limiter: f.deps.Limiter, Audit: f.deps.Audit})
	if err != nil {
		t.Fatal(err)
	}
	f.deps.ReadSync = worker.NewOnDemand(p, f.pool)
	handler := httpserver.New(slog.New(slog.NewTextHandler(io.Discard, nil)), health.NewProbes(nil), f.deps)
	t.Cleanup(handler.Close)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	f.server = server
	read := func(path, token string, want int) map[string]any {
		t.Helper()
		status, body := f.request(t, "GET", path, token, nil)
		if status != want {
			t.Fatalf("%s: %d %s", path, status, body)
		}
		var out map[string]any
		json.Unmarshal(body, &out)
		return out
	}
	read("/.well-known/oauth-protected-resource", "", 200)
	read("/v1/chats", "", 401)
	if session.chats.Load() != 0 {
		t.Fatal("discovery/unauthenticated read synced")
	}
	out := read("/v1/chats?limit=2", f.token, 200)
	if len(out["data"].([]any)) != 0 || session.chats.Load() != 0 {
		t.Fatal("MCP discovered unapproved chats")
	}
	// Browser metadata discovery is explicit and does not fetch history.
	first, err := f.deps.ReadSync.Refresh(context.Background(), f.account, telegram.ReadRequest{Kind: "chats", Limit: 2})
	if err != nil || len(first.ChatIDs) != 1 || first.NextCursor != "0:1" {
		t.Fatalf("metadata page: %v %v", first, err)
	}
	chat := first.ChatIDs[0]
	read("/v1/chats/"+chat+"/messages", f.token, 403)
	if session.history.Load() != 0 {
		t.Fatal("denied chat reached Telegram")
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO chat_permissions(account_id,chat_id,can_read) VALUES($1::uuid,$2::uuid,true)`, f.account, chat); err != nil {
		t.Fatal(err)
	}
	second, err := f.deps.ReadSync.Refresh(context.Background(), f.account, telegram.ReadRequest{Kind: "chats", Limit: 2, Cursor: first.NextCursor})
	if err != nil || second.NextCursor != "1:0" {
		t.Fatalf("boundary: %v %v", second, err)
	}
	third, err := f.deps.ReadSync.Refresh(context.Background(), f.account, telegram.ReadRequest{Kind: "chats", Limit: 2, Cursor: second.NextCursor})
	if err != nil || third.NextCursor != "" {
		t.Fatalf("archive: %v %v", third, err)
	}
	out = read("/v1/chats?limit=2", f.token, 200)
	if len(out["data"].([]any)) != 1 || session.chats.Load() != 3 {
		t.Fatal("allowed listing fetched Telegram or exposed closed chat")
	}
	out = read("/v1/chats/search?q=Requested&limit=2", f.token, 200)
	if len(out["data"].([]any)) != 1 || session.chatSearches.Load() != 0 {
		t.Fatal("AI chat search reached Telegram")
	}
	client, err := f.deps.Access.CreateClient(context.Background(), f.deps.Manager.OwnerID(), "profile-only", "MCP")
	if err != nil {
		t.Fatal(err)
	}
	token, err := f.deps.Access.IssueToken(context.Background(), client, f.account, []access.Scope{access.ScopeProfileRead}, nil)
	if err != nil {
		t.Fatal(err)
	}
	read("/v1/chats/"+chat+"/messages", token.Plaintext, 403)
	read("/v1/chats/00000000-0000-0000-0000-000000000000/messages", f.token, 403)
	read("/v1/chats/"+chat+"/messages?cursor=invalid", f.token, 400)
	if session.history.Load() != 0 {
		t.Fatal("invalid/unauthorized read synced")
	}
	out = read("/v1/chats/"+chat+"/messages?limit=2", f.token, 200)
	if len(out["data"].([]any)) != 2 || out["next_cursor"] == "" {
		t.Fatal("pagination missing")
	}
	read("/v1/chats/"+chat+"/messages?limit=2&cursor="+out["next_cursor"].(string), f.token, 200)
	if session.downloads.Load() != 0 || publisher.calls.Load() != 0 {
		t.Fatal("history started background work or media")
	}
	out = read("/v1/messages/search?q=demand&limit=2&chat_id="+chat, f.token, 200)
	if len(out["data"].([]any)) != 1 || session.searches.Load() != 1 {
		t.Fatal("search mixed cached hits with fresh Telegram matches")
	}
	var mediaID string
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text FROM active_message_media WHERE account_id=$1::uuid LIMIT 1`, f.account).Scan(&mediaID); err != nil {
		t.Fatal(err)
	}
	read("/v1/media/"+mediaID+"/url", f.token, 200)
	if session.downloads.Load() != 1 || publisher.calls.Load() != 0 {
		t.Fatal("requested media was not isolated")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.deps.ReadSync.Refresh(cancelled, f.account, telegram.ReadRequest{Kind: "chats"}); err == nil {
		t.Fatal("cancelled read succeeded")
	}
	if session.chats.Load() != 3 {
		t.Fatal("cancelled read touched Telegram")
	}
	if _, err := f.deps.Limiter.SetAccountCooldown(context.Background(), f.account, time.Minute); err != nil {
		t.Fatal(err)
	}
	read("/v1/chats/"+chat+"/messages?limit=2", f.token, 503)
	if session.chats.Load() != 3 {
		t.Fatal("Telegram FLOOD_WAIT cooldown was bypassed")
	}
}

func (s *demandSession) SearchChatMessages(_ context.Context, chat int64, query string, limit int) ([]telegram.Message, error) {
	if chat != 987 {
		return nil, fmt.Errorf("wrong permitted chat")
	}
	s.searches.Add(1)
	return []telegram.Message{{TelegramMessageID: 99, Type: "Text", Content: ptr("demand match"), SentAt: time.Unix(1700000099, 0)}}, nil
}
