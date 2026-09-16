package tdlib

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeTransport struct {
	recv     chan []byte
	mu       sync.Mutex
	requests []object
	respond  func(object) object
}

func (f *fakeTransport) Send(b []byte) {
	var v object
	json.Unmarshal(b, &v)
	f.mu.Lock()
	f.requests = append(f.requests, v)
	f.mu.Unlock()
	response := f.respond(v)
	response["@extra"] = v["@extra"]
	raw, _ := json.Marshal(response)
	f.recv <- raw
}
func (f *fakeTransport) Receive() []byte {
	select {
	case b := <-f.recv:
		return b
	case <-time.After(5 * time.Millisecond):
		return nil
	}
}
func (f *fakeTransport) Destroy() {}
func TestDecodeVoiceAndVideoNotes(t *testing.T) {
	for _, tc := range []struct{ kind, key, file string }{{"messageVoiceNote", "voice_note", "voice"}, {"messageVideoNote", "video_note", "video"}} {
		m := decodeMessage(object{"id": int64(1), "date": int64(1), "content": object{"@type": tc.kind, tc.key: object{tc.file: object{"id": 42, "size": 100}}}})
		if len(m.Media) != 1 || m.Media[0].TelegramFileID != 42 {
			t.Fatalf("%s media missing", tc.kind)
		}
	}
}
func TestHistoryFiltersCursorAndKeepsShortPage(t *testing.T) {
	f := &fakeTransport{recv: make(chan []byte, 10), respond: func(q object) object {
		return object{"@type": "messages", "messages": []any{object{"id": int64(100), "date": int64(1), "content": object{"@type": "messageText", "text": object{"text": "cursor"}}}, object{"id": int64(99), "date": int64(1), "content": object{"@type": "messageText", "text": object{"text": "older"}}}}}
	}}
	s := &Session{rpc: newRPC(f, nil)}
	defer s.rpc.close()
	items, err := s.GetChatHistory(context.Background(), 1, 100, 100)
	if err != nil || len(items) != 1 || items[0].TelegramMessageID != 99 {
		t.Fatalf("page=%v err=%v", items, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if num(f.requests[0]["offset"]) != 0 {
		t.Fatal("history must not request newer messages")
	}
}
func TestNativeEncryptedSession(t *testing.T) {
	if os.Getenv("TDLIB_LIBRARY") == "" {
		t.Skip("TDLIB_LIBRARY not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	s, err := Open(ctx, Options{AccountID: "test-account", APIID: 1, APIHash: strings.Repeat("a", 32), MasterKey: []byte(strings.Repeat("k", 32)), Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for {
		state := s.State()
		if state["state"] == "authorizationStateWaitPhoneNumber" {
			return
		}
		if state["error"] != "" {
			t.Fatal(state["error"])
		}
		select {
		case <-ctx.Done():
			t.Fatalf("session did not become ready for login: %v", state)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestMembersResumeWithoutChatListCache(t *testing.T) {
	f := &fakeTransport{recv: make(chan []byte, 10), respond: func(q object) object {
		switch str(q["@type"]) {
		case "getChat":
			return object{"@type": "chat", "id": int64(-42), "type": object{"@type": "chatTypeBasicGroup", "basic_group_id": int64(42)}}
		case "getBasicGroupFullInfo":
			return object{"@type": "basicGroupFullInfo", "members": []any{object{"member_id": object{"@type": "messageSenderUser", "user_id": int64(7)}, "status": object{"@type": "chatMemberStatusMember"}}}}
		default:
			return object{"@type": "error", "code": 400, "message": "unexpected request"}
		}
	}}
	s := &Session{rpc: newRPC(f, nil)}
	defer s.rpc.close()
	page, err := s.ListMembers(context.Background(), -42, "", 100)
	if err != nil || len(page.Items) != 1 || page.Items[0].TelegramPeerID != 7 {
		t.Fatalf("resumed member page=%v err=%v", page, err)
	}
}

func TestResolveMessageFileUsesCurrentIDAndStableIdentity(t *testing.T) {
	f := &fakeTransport{recv: make(chan []byte, 10), respond: func(q object) object {
		return object{"@type": "message", "id": int64(100), "date": int64(1), "content": object{"@type": "messageDocument", "document": object{"document": object{"id": int64(999), "remote": object{"unique_id": "stable-file"}}}}}
	}}
	s := &Session{rpc: newRPC(f, nil)}
	defer s.rpc.close()
	id, err := s.ResolveMessageFile(context.Background(), 1, 100, "stable-file", "document")
	if err != nil || id != 999 {
		t.Fatalf("resolved id=%d err=%v", id, err)
	}
	if _, err := s.ResolveMessageFile(context.Background(), 1, 100, "different-file", "document"); err == nil {
		t.Fatal("replaced attachment must not be downloaded as the old file")
	}
}
