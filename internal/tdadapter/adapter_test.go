package tdadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/tdjson"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
)

const accountID = "11111111-1111-4111-8111-111111111111"

type readerFunc func(context.Context, string, map[string]any) (json.RawMessage, error)

func (f readerFunc) Read(c context.Context, m string, p map[string]any) (json.RawMessage, error) {
	return f(c, m, p)
}
func fixture(t *testing.T, fn readerFunc) (*Adapter, *Index) {
	t.Helper()
	index := NewIndex()
	a, err := New(fn, index, Config{AccountID: accountID, FilesDirectory: t.TempDir(), MaxFileBytes: 1 << 20, Authorize: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a, index
}
func jsonRaw(v any) json.RawMessage {
	r, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return r
}
func chatJSON(id int64) json.RawMessage {
	return jsonRaw(map[string]any{"@type": "chat", "id": id, "title": "Test", "type": map[string]any{"@type": "chatTypePrivate"}})
}
func msg(id int64, typ string) map[string]any {
	return map[string]any{"id": id, "chat_id": int64(10), "date": 1700000000, "sender_id": map[string]any{"@type": "messageSenderUser", "user_id": 1}, "content": map[string]any{"@type": typ, "text": map[string]any{"text": "hello", "entities": []any{}}}}
}
func historyFixture(t *testing.T, items []map[string]any) (*Adapter, *[]map[string]any) {
	t.Helper()
	if items == nil {
		items = []map[string]any{}
	}
	calls := []map[string]any{}
	a, _ := fixture(t, func(_ context.Context, m string, p map[string]any) (json.RawMessage, error) {
		calls = append(calls, p)
		switch m {
		case "getChat":
			return chatJSON(10), nil
		case "getChatHistory":
			return jsonRaw(map[string]any{"@type": "messages", "messages": items}), nil
		}
		return nil, fmt.Errorf("unexpected method %s", m)
	})
	return a, &calls
}
func TestHistoryPageAdvancesAcrossFullyExcludedContent(t *testing.T) {
	a, _ := historyFixture(t, []map[string]any{msg(90, "messageUnsupported"), msg(80, "messageExpiredPhoto")})
	page, err := a.GetChatHistory(context.Background(), 10, 100, 30)
	if err != nil || page.Exhausted || len(page.Items) != 0 || page.NextBeforeMessageID != 80 || page.SourceCount != 2 {
		t.Fatal(page, err)
	}
	if err := page.Validate(100, 30); err != nil {
		t.Fatal(err)
	}
}
func TestHistoryShortPageIsNotEOF(t *testing.T) {
	a, calls := historyFixture(t, []map[string]any{msg(90, "messageText")})
	p, err := a.GetChatHistory(context.Background(), 10, 100, 30)
	if err != nil || p.Exhausted || len(p.Items) != 1 || p.NextBeforeMessageID != 90 {
		t.Fatal(p, err)
	}
	if (*calls)[1]["only_local"] != false || (*calls)[1]["from_message_id"] != int64(100) {
		t.Fatal(*calls)
	}
}
func TestHistoryAnchorDuplicatesAndLimits(t *testing.T) {
	for _, items := range [][]map[string]any{
		{msg(100, "messageText"), msg(90, "messageText"), msg(80, "messageText")},
		{msg(90, "messageText"), msg(80, "messageText"), msg(70, "messageText")},
		{msg(90, "messageText"), msg(90, "messageText"), msg(80, "messageText")},
	} {
		a, _ := historyFixture(t, items)
		p, err := a.GetChatHistory(context.Background(), 10, 100, 2)
		if err != nil || len(p.Items) != 2 || p.NextBeforeMessageID != 80 {
			t.Fatal(p, err)
		}
	}
}
func TestHistoryEmptyIsEOFButAnchorOnlyIsPending(t *testing.T) {
	a, _ := historyFixture(t, nil)
	p, err := a.GetChatHistory(context.Background(), 10, 100, 30)
	if err != nil || !p.Exhausted || p.NextBeforeMessageID != 0 {
		t.Fatal(p, err)
	}
	a, _ = historyFixture(t, []map[string]any{msg(100, "messageText")})
	_, err = a.GetChatHistory(context.Background(), 10, 100, 30)
	var pending *Pending
	if !errors.As(err, &pending) {
		t.Fatal(err)
	}
}
func TestHistoryRejectsCrossChatMalformedAndUnboundedResponses(t *testing.T) {
	cross := msg(90, "messageText")
	cross["chat_id"] = int64(999)
	for _, items := range [][]map[string]any{{cross}, {msg(-1, "messageText")}, {msg(110, "messageText")}, {msg(90, "messageText"), msg(80, "messageText"), msg(70, "messageText")}} {
		a, _ := historyFixture(t, items)
		_, err := a.GetChatHistory(context.Background(), 10, 100, 1)
		if !errors.Is(err, ErrInvalidResponse) {
			t.Fatal(err)
		}
	}
}
func TestHistoryNeverRetainsExpiringOrProtectedMessages(t *testing.T) {
	for _, field := range []string{"auto_delete_in", "ttl", "has_protected_content", "self_destruct_type"} {
		value := msg(90, "messageText")
		switch field {
		case "has_protected_content":
			value[field] = true
		case "self_destruct_type":
			value[field] = map[string]any{"@type": "messageSelfDestructTypeImmediately"}
		default:
			value[field] = 10
		}
		a, _ := historyFixture(t, []map[string]any{value})
		p, err := a.GetChatHistory(context.Background(), 10, 100, 30)
		if err != nil || len(p.Items) != 0 || p.NextBeforeMessageID != 90 {
			t.Fatal(field, p, err)
		}
	}
}
func TestHistoryRejectsExcludedChatsBeforeFetchingMessages(t *testing.T) {
	for _, typ := range []string{"chatTypeSecret", "protected", "auto-delete", "unknown"} {
		calls := 0
		a, _ := fixture(t, func(_ context.Context, m string, _ map[string]any) (json.RawMessage, error) {
			calls++
			if m != "getChat" {
				t.Fatal("queried excluded history")
			}
			r := map[string]any{"@type": "chat", "id": 10, "type": map[string]any{"@type": "chatTypePrivate"}}
			switch typ {
			case "protected":
				r["has_protected_content"] = true
			case "auto-delete":
				r["message_auto_delete_time"] = 60
			default:
				r["type"] = map[string]any{"@type": typ}
			}
			return jsonRaw(r), nil
		})
		if _, err := a.GetChatHistory(context.Background(), 10, 0, 30); !errors.Is(err, ErrExcluded) || calls != 1 {
			t.Fatal(err, calls)
		}
	}
}
func TestMediaMappingDoesNotLeakPathsOrReplyContent(t *testing.T) {
	value := msg(90, "messageDocument")
	value["content"] = map[string]any{"@type": "messageDocument", "caption": map[string]any{"text": "caption", "entities": []any{}}, "document": map[string]any{"file_name": "a.pdf", "mime_type": "application/pdf", "document": map[string]any{"id": 17, "size": 123, "remote": map[string]any{"unique_id": "unique", "id": "SECRET_REMOTE"}, "local": map[string]any{"path": "/private/path"}}}}
	value["reply_to"] = map[string]any{"@type": "messageReplyToMessage", "chat_id": 10, "message_id": 2, "quote": "SECRET_QUOTE"}
	a, _ := historyFixture(t, []map[string]any{value})
	p, err := a.GetChatHistory(context.Background(), 10, 0, 10)
	if err != nil || len(p.Items) != 1 || len(p.Items[0].Media) != 1 || p.Items[0].Media[0].TelegramFileID != 17 {
		t.Fatal(p, err)
	}
	body := string(jsonRaw(p))
	for _, secret := range []string{"SECRET_REMOTE", "SECRET_QUOTE", "/private/path"} {
		if strings.Contains(body, secret) {
			t.Fatal("unsafe raw metadata retained")
		}
	}
}
func TestAdapterAuthorizationAndResponseTypes(t *testing.T) {
	calls := 0
	a, _ := fixture(t, func(context.Context, string, map[string]any) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"@type":"wrong","id":1}`), nil
	})
	denied := errors.New("lease expired")
	a.cfg.Authorize = func(context.Context) error { return denied }
	if _, err := a.Profile(context.Background()); !errors.Is(err, denied) || calls != 0 {
		t.Fatal(err, calls)
	}
	a.cfg.Authorize = func(context.Context) error { return nil }
	if _, err := a.Profile(context.Background()); !errors.Is(err, ErrInvalidResponse) {
		t.Fatal(err)
	}
}
func observe(t *testing.T, i *Index, v any) {
	t.Helper()
	if err := i.Observe(context.Background(), jsonRaw(v)); err != nil {
		t.Fatal(err)
	}
}
func addChat(t *testing.T, i *Index, id int64, order string, list string) {
	observe(t, i, map[string]any{"@type": "updateNewChat", "chat": map[string]any{"id": id, "title": fmt.Sprint(id), "type": map[string]any{"@type": "chatTypePrivate"}, "positions": []any{map[string]any{"list": map[string]any{"@type": list}, "order": order}}}})
}
func TestChatIndexMainArchiveAndLargeIntegerOrder(t *testing.T) {
	a, i := fixture(t, func(_ context.Context, m string, _ map[string]any) (json.RawMessage, error) {
		if m != "loadChats" {
			t.Fatal(m)
		}
		return nil, &tdjson.Error{Code: 404}
	})
	addChat(t, i, 10, "9007199254740993", "chatListMain")
	addChat(t, i, 20, "9007199254740992", "chatListMain")
	addChat(t, i, 30, "1", "chatListArchive")
	var ids []int64
	cursor := ""
	for n := 0; n < 5; n++ {
		p, err := a.ListChats(context.Background(), cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range p.Items {
			ids = append(ids, c.TelegramChatID)
		}
		cursor = p.NextCursor
		if cursor == "" {
			break
		}
	}
	if fmt.Sprint(ids) != "[10 20 30]" || cursor != "" {
		t.Fatal(ids, cursor)
	}
}
func TestChatIndexPositionsAndProtectionUpdates(t *testing.T) {
	a, i := fixture(t, func(context.Context, string, map[string]any) (json.RawMessage, error) {
		return nil, &tdjson.Error{Code: 404}
	})
	addChat(t, i, 10, "100", "chatListMain")
	observe(t, i, map[string]any{"@type": "updateChatPosition", "chat_id": 10, "position": map[string]any{"list": map[string]any{"@type": "chatListMain"}, "order": "0"}})
	p, err := a.ListChats(context.Background(), "", 30)
	if err != nil || len(p.Items) != 0 {
		t.Fatal(p, err)
	}
	addChat(t, i, 10, "100", "chatListMain")
	observe(t, i, map[string]any{"@type": "updateChatHasProtectedContent", "chat_id": 10, "has_protected_content": true})
	p, err = a.ListChats(context.Background(), "", 30)
	if err != nil || len(p.Items) != 0 {
		t.Fatal(p, err)
	}
}
func TestChatLoadingIsBoundedWithoutFalseEOF(t *testing.T) {
	calls := 0
	a, _ := fixture(t, func(context.Context, string, map[string]any) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"@type":"ok"}`), nil
	})
	_, err := a.ListChats(context.Background(), "", 30)
	var pending *Pending
	if !errors.As(err, &pending) || calls != 4 {
		t.Fatal(err, calls)
	}
}
func TestCursorsAreBoundToAccountAndOperation(t *testing.T) {
	a, _ := fixture(t, func(context.Context, string, map[string]any) (json.RawMessage, error) {
		t.Fatal("invalid cursor caused native request")
		return nil, nil
	})
	for _, raw := range []string{"bad", strings.Repeat("x", 513), encodeCursor(cursorWire{Version: 1, Account: "other", Kind: "chats"}), encodeCursor(cursorWire{Version: 1, Account: accountID, Kind: "members", ChatID: 10})} {
		if _, err := a.ListChats(context.Background(), raw, 30); !errors.Is(err, ErrCursor) {
			t.Fatal(err)
		}
	}
}
func TestContactsUseUpdateCacheAndExcludePhoneData(t *testing.T) {
	a, i := fixture(t, func(_ context.Context, m string, _ map[string]any) (json.RawMessage, error) {
		if m != "getContacts" {
			t.Fatal("unexpected contact fanout", m)
		}
		return json.RawMessage(`{"@type":"users","user_ids":[1,1]}`), nil
	})
	observe(t, i, map[string]any{"@type": "updateUser", "user": map[string]any{"id": 1, "first_name": "A", "phone_number": "PRIVATE_PHONE", "is_mutual_contact": true}})
	rows, err := a.ListContacts(context.Background())
	if err != nil || len(rows) != 1 || rows[0].FirstName != "A" || strings.Contains(string(jsonRaw(rows)), "PRIVATE_PHONE") {
		t.Fatal(rows, err)
	}
}
func TestMemberCursorAdvancesPastExcludedRoles(t *testing.T) {
	offsets := []any{}
	a, _ := fixture(t, func(_ context.Context, m string, p map[string]any) (json.RawMessage, error) {
		if m == "getChat" {
			return json.RawMessage(`{"@type":"chat","id":10,"type":{"@type":"chatTypeSupergroup","supergroup_id":20}}`), nil
		}
		if m != "getSupergroupMembers" || p["supergroup_id"] != int64(20) {
			t.Fatal(m, p)
		}
		offsets = append(offsets, p["offset"])
		if p["offset"] == 1 {
			return json.RawMessage(`{"@type":"chatMembers","members":[]}`), nil
		}
		return json.RawMessage(`{"@type":"chatMembers","members":[{"member_id":{"@type":"messageSenderUser","user_id":1},"status":{"@type":"chatMemberStatusBanned"}}]}`), nil
	})
	p, err := a.ListMembers(context.Background(), 10, "", 30)
	if err != nil || len(p.Items) != 0 || p.NextCursor == "" {
		t.Fatal(p, err)
	}
	p, err = a.ListMembers(context.Background(), 10, p.NextCursor, 30)
	if err != nil || p.NextCursor != "" || fmt.Sprint(offsets) != "[0 1]" {
		t.Fatal(p, offsets, err)
	}
}
func downloadFixture(t *testing.T) (*Adapter, *fileWire) {
	t.Helper()
	f := &fileWire{ID: 7, Size: 5}
	f.Local.Complete = true
	var a *Adapter
	a, _ = fixture(t, func(_ context.Context, m string, p map[string]any) (json.RawMessage, error) {
		if m == "downloadFile" && (p["limit"] != int64(1<<20)+1 || p["synchronous"] != true) {
			t.Fatal("uncapped download", p)
		}
		raw := map[string]any{}
		_ = json.Unmarshal(jsonRaw(f), &raw)
		raw["@type"] = "file"
		return jsonRaw(raw), nil
	})
	f.Local.Path = filepath.Join(a.cfg.FilesDirectory, "file.bin")
	if err := os.WriteFile(f.Local.Path, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	return a, f
}
func TestDownloadUsesPrivateRootAndExactLength(t *testing.T) {
	a, _ := downloadFixture(t)
	d, err := a.DownloadFile(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Reader.Close()
	body, err := io.ReadAll(d.Reader)
	if err != nil || string(body) != "hello" || d.Size != 5 {
		t.Fatal(string(body), d, err)
	}
}
func TestDownloadRejectsTraversalSymlinksAndWrongSizes(t *testing.T) {
	for _, kind := range []string{"outside", "symlink", "size", "directory", "fifo", "too-large", "wrong-id"} {
		t.Run(kind, func(t *testing.T) {
			a, f := downloadFixture(t)
			switch kind {
			case "outside":
				f.Local.Path = "/etc/passwd"
			case "symlink":
				name := filepath.Join(a.cfg.FilesDirectory, "escape")
				if err := os.Symlink("/etc/passwd", name); err != nil {
					t.Fatal(err)
				}
				f.Local.Path = name
			case "size":
				f.Size = 10
			case "directory":
				f.Local.Path = a.cfg.FilesDirectory
			case "fifo":
				f.Local.Path = filepath.Join(a.cfg.FilesDirectory, "fifo")
				if err := syscall.Mkfifo(f.Local.Path, 0600); err != nil {
					t.Fatal(err)
				}
			case "too-large":
				f.Size = 2 << 20
			case "wrong-id":
				f.ID = 8
			}
			if d, err := a.DownloadFile(context.Background(), 7); err == nil {
				d.Reader.Close()
				t.Fatal("unsafe file accepted")
			}
		})
	}
}
func TestDownloadIncompleteAndRevokedStream(t *testing.T) {
	a, f := downloadFixture(t)
	f.Local.Complete = false
	_, err := a.DownloadFile(context.Background(), 7)
	var pending *Pending
	if !errors.As(err, &pending) {
		t.Fatal(err)
	}
	f.Local.Complete = true
	var authorizationError error
	a.cfg.Authorize = func(context.Context) error { return authorizationError }
	d, err := a.DownloadFile(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Reader.Close()
	revoked := errors.New("ownership revoked")
	authorizationError = revoked
	if n, err := d.Reader.Read(make([]byte, 5)); n != 0 || !errors.Is(err, revoked) {
		t.Fatal(n, err)
	}
}
func TestHistoryPageValidation(t *testing.T) {
	good := telegram.HistoryPage{Items: []telegram.Message{}, SourceCount: 1, NextBeforeMessageID: 80}
	if err := good.Validate(100, 30); err != nil {
		t.Fatal(err)
	}
	bad := []telegram.HistoryPage{{}, {Exhausted: true, SourceCount: 1}, {SourceCount: 1, NextBeforeMessageID: 100}, {SourceCount: 1, NextBeforeMessageID: 80, Items: []telegram.Message{{TelegramMessageID: 70, SentAt: time.Now()}}}}
	for _, p := range bad {
		if err := p.Validate(100, 30); err == nil {
			t.Fatal("invalid page accepted", p)
		}
	}
}

func TestMissingCollectionsDoNotMeanExhausted(t *testing.T) {
	for _, value := range []string{`{"@type":"messages"}`, `{"@type":"messages","messages":null}`} {
		a, _ := fixture(t, func(_ context.Context, m string, _ map[string]any) (json.RawMessage, error) {
			if m == "getChat" {
				return chatJSON(10), nil
			}
			return json.RawMessage(value), nil
		})
		if _, err := a.GetChatHistory(context.Background(), 10, 0, 10); !errors.Is(err, ErrInvalidResponse) {
			t.Fatal(err)
		}
	}
	for _, value := range []string{`{"@type":"users"}`, `{"@type":"users","user_ids":null}`} {
		a, _ := fixture(t, func(context.Context, string, map[string]any) (json.RawMessage, error) {
			return json.RawMessage(value), nil
		})
		if _, err := a.ListContacts(context.Background()); !errors.Is(err, ErrInvalidResponse) {
			t.Fatal(err)
		}
	}
	for _, typ := range []string{"chatTypeSupergroup", "chatTypeBasicGroup"} {
		a, _ := fixture(t, func(_ context.Context, m string, _ map[string]any) (json.RawMessage, error) {
			if m == "getChat" {
				return jsonRaw(map[string]any{"@type": "chat", "id": 10, "type": map[string]any{"@type": typ, "supergroup_id": 1, "basic_group_id": 2}}), nil
			}
			if m == "getSupergroupMembers" {
				return json.RawMessage(`{"@type":"chatMembers"}`), nil
			}
			return json.RawMessage(`{"@type":"basicGroupFullInfo","members":null}`), nil
		})
		if _, err := a.ListMembers(context.Background(), 10, "", 10); !errors.Is(err, ErrInvalidResponse) {
			t.Fatal(typ, err)
		}
	}
}

func TestMalformedSupportedContentIsNotSilentlySaved(t *testing.T) {
	for _, typ := range []string{"messageText", "messagePhoto", "messageDocument", "messageVoiceNote"} {
		v := msg(90, typ)
		v["content"] = map[string]any{"@type": typ}
		a, _ := historyFixture(t, []map[string]any{v})
		if _, err := a.GetChatHistory(context.Background(), 10, 0, 10); !errors.Is(err, ErrInvalidResponse) {
			t.Fatal(typ, err)
		}
	}
}

func TestConstructorRequiresBoundAuthorizationAndPrivateRoot(t *testing.T) {
	r := readerFunc(func(context.Context, string, map[string]any) (json.RawMessage, error) {
		t.Fatal("unexpected native call")
		return nil, nil
	})
	base := Config{AccountID: accountID, FilesDirectory: t.TempDir(), MaxFileBytes: 1024, Authorize: func(context.Context) error { return nil }}
	for _, name := range []string{"account", "authorize", "root", "size", "oversize"} {
		cfg := base
		switch name {
		case "account":
			cfg.AccountID = "../other"
		case "authorize":
			cfg.Authorize = nil
		case "root":
			cfg.FilesDirectory = "relative/path"
		case "size":
			cfg.MaxFileBytes = 0
		case "oversize":
			cfg.MaxFileBytes = 5 << 30
		}
		if a, err := New(r, NewIndex(), cfg); err == nil {
			a.Close()
			t.Fatal("accepted", name)
		}
	}
}

func TestSingleAccountRegistryRejectsOtherAccount(t *testing.T) {
	a, _ := fixture(t, func(context.Context, string, map[string]any) (json.RawMessage, error) {
		t.Fatal("native call in lookup")
		return nil, nil
	})
	if s, err := a.Get(context.Background(), accountID); err != nil || s != a {
		t.Fatal(s, err)
	}
	if _, err := a.Get(context.Background(), "22222222-2222-4222-8222-222222222222"); err == nil {
		t.Fatal("cross-account registry access")
	}
}
func TestReadRechecksOwnershipAfterResponse(t *testing.T) {
	lost := false
	a, _ := fixture(t, func(context.Context, string, map[string]any) (json.RawMessage, error) {
		lost = true
		return json.RawMessage(`{"@type":"user","id":1}`), nil
	})
	revoked := errors.New("owner lost")
	a.cfg.Authorize = func(context.Context) error {
		if lost {
			return revoked
		}
		return nil
	}
	if _, err := a.Profile(context.Background()); !errors.Is(err, revoked) {
		t.Fatal(err)
	}
}

func TestIndexCannotBeSharedAcrossAdapters(t *testing.T) {
	a, index := fixture(t, func(context.Context, string, map[string]any) (json.RawMessage, error) { return nil, nil })
	for _, id := range []string{accountID, "22222222-2222-4222-8222-222222222222"} {
		cfg := a.cfg
		cfg.AccountID = id
		if other, err := New(a.reader, index, cfg); err == nil {
			other.Close()
			t.Fatal("reused a native index")
		}
	}
}
func TestCursorMustHaveVersionAndCurrentSessionGeneration(t *testing.T) {
	r := readerFunc(func(context.Context, string, map[string]any) (json.RawMessage, error) { return nil, nil })
	a, _ := fixture(t, r)
	b, _ := fixture(t, r)
	c, err := a.cursor("", "chats", 0)
	if err != nil {
		t.Fatal(err)
	}
	raw := encodeCursor(c)
	if _, err := a.cursor(raw, "chats", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := b.cursor(raw, "chats", 0); !errors.Is(err, ErrCursor) {
		t.Fatal("old-session cursor accepted", err)
	}
	if _, err := a.cursor("e30", "chats", 0); !errors.Is(err, ErrCursor) {
		t.Fatal("empty JSON cursor accepted", err)
	}
}

func TestDownloadReadCancellationTruncationAndChunkBounds(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		a, _ := downloadFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		d, err := a.DownloadFile(ctx, 7)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Reader.Close()
		cancel()
		if n, err := d.Reader.Read(make([]byte, 5)); n != 0 || !errors.Is(err, context.Canceled) {
			t.Fatal(n, err)
		}
	})
	t.Run("truncated", func(t *testing.T) {
		a, f := downloadFixture(t)
		d, err := a.DownloadFile(context.Background(), 7)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Reader.Close()
		if err := os.Truncate(f.Local.Path, 2); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(d.Reader); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatal(err)
		}
	})
	t.Run("bounded", func(t *testing.T) {
		a, f := downloadFixture(t)
		data := make([]byte, 128<<10)
		if err := os.WriteFile(f.Local.Path, data, 0600); err != nil {
			t.Fatal(err)
		}
		f.Size = int64(len(data))
		d, err := a.DownloadFile(context.Background(), 7)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Reader.Close()
		if n, err := d.Reader.Read(data); n != 64<<10 || err != nil {
			t.Fatal(n, err)
		}
	})
}
