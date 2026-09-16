package integration

import (
	"context"
	"github.com/JavoxirJava/telegram-gateway/internal/chats"
	"github.com/JavoxirJava/telegram-gateway/internal/media"
	"github.com/JavoxirJava/telegram-gateway/internal/messages"
	"github.com/JavoxirJava/telegram-gateway/internal/syncjob"
	"github.com/JavoxirJava/telegram-gateway/internal/syncstate"
	"github.com/JavoxirJava/telegram-gateway/internal/tdlib"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/JavoxirJava/telegram-gateway/internal/worker"
	"io"
	"strings"
	"testing"
	"time"
)

type resumedMediaSession struct {
	historySession
	t        *testing.T
	resolved bool
}

func (s *resumedMediaSession) ResolveMessageFile(_ context.Context, chatID, messageID int64, key, kind string) (int64, error) {
	if chatID != 987 || messageID != 333 || key != "same-attachment" || kind != "document" {
		s.t.Fatal("media identity did not survive queue persistence")
	}
	s.resolved = true
	return 999, nil
}
func (s *resumedMediaSession) DownloadFile(_ context.Context, id int64) (telegram.Download, error) {
	if !s.resolved || id != 999 {
		s.t.Fatal("download used a stale file ID")
	}
	return telegram.Download{Reader: io.NopCloser(strings.NewReader("resumed-media")), Size: 13, ContentType: "text/plain"}, nil
}

func TestMediaSurvivesSessionFileIDChange(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	cid, err := f.deps.Chats.Upsert(ctx, chats.Chat{AccountID: f.account, TelegramChatID: 987, ChatType: "private"})
	if err != nil {
		t.Fatal(err)
	}
	mid, err := f.deps.Messages.Upsert(ctx, messages.Message{AccountID: f.account, ChatID: cid, TelegramMessageID: 333, MessageType: "Document", SentAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	repo := media.NewRepository(f.pool)
	oldID, newID := int64(10), int64(20)
	key := "same-attachment"
	first, err := repo.RegisterPending(ctx, mid, "document", &oldID, &key, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.RegisterPending(ctx, mid, "document", &newID, &key, nil, nil, nil)
	if err != nil || first != second {
		t.Fatalf("stable media duplicated: %s %s %v", first, second, err)
	}
	s := &resumedMediaSession{t: t}
	p, err := worker.NewProcessor(worker.Dependencies{WorkerID: "resumed-worker", Sessions: fakeSessions{s}, Accounts: f.deps.Accounts, Chats: f.deps.Chats, Messages: f.deps.Messages, Contacts: f.deps.Contacts, Members: f.deps.Members, MediaRepo: repo, Media: f.deps.Media, SyncStates: syncstate.NewRepository(f.pool), Publisher: &fakePublisher{}, Limiter: f.deps.Limiter, Audit: f.deps.Audit})
	if err != nil {
		t.Fatal(err)
	}
	e, err := syncjob.NewEnvelope(syncjob.KindMediaDownload, f.account, "", syncjob.MediaDownloadPayload{MediaID: first, MessageID: mid, TelegramFileID: oldID})
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
	item, err := repo.GetActive(ctx, f.account, first)
	if err != nil || item.DownloadStatus != "ready" {
		t.Fatalf("media not ready: %v %v", item, err)
	}
}

type fakeSessions struct{ s telegram.Session }

func (f fakeSessions) Get(context.Context, string) (telegram.Session, error) { return f.s, nil }

type historySession struct{}

func (historySession) Profile(context.Context) (telegram.Profile, error) {
	return telegram.Profile{}, nil
}
func (historySession) ListChats(context.Context, string, int) (telegram.ChatPage, error) {
	return telegram.ChatPage{}, nil
}
func (historySession) ListContacts(context.Context) ([]telegram.Contact, error) { return nil, nil }
func (historySession) ListMembers(context.Context, int64, string, int) (telegram.MemberPage, error) {
	return telegram.MemberPage{}, nil
}
func (historySession) DownloadFile(context.Context, int64) (telegram.Download, error) {
	return telegram.Download{}, nil
}
func (historySession) GetChatHistory(_ context.Context, _ int64, before int64, _ int) ([]telegram.Message, error) {
	id := int64(200)
	if before == 200 {
		id = 199
	}
	if before == 199 {
		return nil, nil
	}
	return []telegram.Message{{TelegramMessageID: id, Type: "Text", Content: ptr("history"), SentAt: time.Now().Add(-time.Hour)}}, nil
}

type fakePublisher struct{ pages []syncjob.ChatHistoryPayload }

func (f *fakePublisher) EnqueueAccountBootstrapPage(context.Context, string, string) error {
	return nil
}
func (f *fakePublisher) EnqueueContactsSync(context.Context, string) error { return nil }
func (f *fakePublisher) EnqueueChatHistory(_ context.Context, _ string, p syncjob.ChatHistoryPayload) error {
	f.pages = append(f.pages, p)
	return nil
}
func (f *fakePublisher) EnqueueChatMembers(context.Context, string, syncjob.ChatMembersPayload) error {
	return nil
}
func (f *fakePublisher) EnqueueMediaDownload(context.Context, string, syncjob.MediaDownloadPayload) error {
	return nil
}
func TestWorkerJournalAndBackfill(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	publisher := &fakePublisher{}
	p, err := worker.NewProcessor(worker.Dependencies{WorkerID: "integration-worker", Sessions: fakeSessions{historySession{}}, Accounts: f.deps.Accounts, Chats: f.deps.Chats, Messages: f.deps.Messages, Contacts: f.deps.Contacts, Members: f.deps.Members, MediaRepo: media.NewRepository(f.pool), Media: f.deps.Media, SyncStates: syncstate.NewRepository(f.pool), Publisher: publisher, Limiter: f.deps.Limiter, Audit: f.deps.Audit})
	if err != nil {
		t.Fatal(err)
	}
	chatID, err := f.deps.Chats.Upsert(ctx, chats.Chat{AccountID: f.account, TelegramChatID: 7654, ChatType: "private"})
	if err != nil {
		t.Fatal(err)
	}
	payload := syncjob.ChatHistoryPayload{ChatID: chatID, TelegramChatID: 7654, RequestedPageSize: 100}
	for i := 0; i < 3; i++ {
		envelope, err := syncjob.NewEnvelope(syncjob.KindChatHistory, f.account, "", payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Handle(ctx, envelope); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			if len(publisher.pages) != i+1 {
				t.Fatal("short history page stopped backfill")
			}
			payload = publisher.pages[i]
		}
	}
	state, err := syncstate.NewRepository(f.pool).Ensure(ctx, f.account, &chatID, "history")
	if err != nil {
		t.Fatal(err)
	}
	if *state.NewestMessageID != 200 || *state.OldestMessageID != 199 {
		t.Fatalf("history bounds %v", state)
	}
	if err := p.ProcessUpdate(ctx, f.pool, f.account, tdlib.Event{Kind: "updateMessageContent", TelegramChatID: 7654, MessageID: 200, Content: ptr("edited")}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.deps.Messages.Upsert(ctx, messages.Message{AccountID: f.account, ChatID: chatID, TelegramMessageID: 200, MessageType: "Text", Content: ptr("stale history"), SentAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	items, err := f.deps.Messages.ListActiveByChat(ctx, f.account, chatID, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.TelegramMessageID == 200 && *item.Content != "edited" {
			t.Fatal("late history overwrote live edit")
		}
	}
	if err := p.ProcessUpdate(ctx, f.pool, f.account, tdlib.Event{Kind: "updateDeleteMessages", TelegramChatID: 7654, MessageIDs: []int64{201}, Permanent: true}); err != nil {
		t.Fatal(err)
	}
	if err := p.StoreMessage(ctx, f.account, chatID, telegram.Message{TelegramMessageID: 201, Type: "Text", Content: ptr("deleted before backfill"), SentAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	items, err = f.deps.Messages.ListActiveByChat(ctx, f.account, chatID, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.TelegramMessageID == 201 {
			t.Fatal("late backfill resurrected deleted message")
		}
	}
}
