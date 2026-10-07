package tdlib

import (
	"context"
	"testing"
)

func TestChatSearchUsesServerWithoutEnumeratingDialogs(t *testing.T) {
	f := &fakeTransport{recv: make(chan []byte, 10), respond: func(q object) object {
		switch str(q["@type"]) {
		case "searchChatsOnServer":
			if str(q["query"]) != "MD Pro" || num(q["limit"]) != 5 {
				t.Error("wrong search parameters")
			}
			return object{"@type": "chats", "chat_ids": []any{int64(42)}}
		case "getChat":
			if num(q["chat_id"]) != 42 {
				t.Error("wrong chat identity")
			}
			return object{"id": int64(42), "title": "MD Pro", "type": object{"@type": "chatTypeBasicGroup"}}
		default:
			t.Error("search enumerated chats", q["@type"])
			return object{"@type": "error", "code": 500, "message": "unexpected"}
		}
	}}
	s := &Session{rpc: newRPC(f, nil)}
	defer s.rpc.close()
	items, err := s.SearchChats(context.Background(), "MD Pro", 5)
	if err != nil || len(items) != 1 || items[0].TelegramChatID != 42 {
		t.Fatalf("search: %v %v", items, err)
	}
}

func TestChatListKeepsNativePageContinuation(t *testing.T) {
	f := &fakeTransport{recv: make(chan []byte, 10), respond: func(q object) object {
		switch str(q["@type"]) {
		case "loadChats":
			return object{"@type": "error", "code": 404, "message": "loaded"}
		case "getChats":
			if num(q["limit"]) != 3 {
				t.Error("offset and page limit not used")
			}
			return object{"@type": "chats", "chat_ids": []any{int64(1), int64(2), int64(3), int64(4)}}
		default:
			t.Error("unexpected call")
			return object{"@type": "ok"}
		}
	}}
	s := &Session{rpc: newRPC(f, nil), chats: map[int64]object{3: {"id": int64(3), "type": object{"@type": "chatTypePrivate"}}}}
	defer s.rpc.close()
	page, err := s.ListChats(context.Background(), "0:2", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].TelegramChatID != 3 || page.NextCursor != "0:3" {
		t.Fatalf("page: %v %v", page, err)
	}
}

func TestCancelledDownloadStopsNativeTransfer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeTransport{recv: make(chan []byte, 10), respond: func(q object) object {
		if str(q["@type"]) == "downloadFile" {
			cancel()
			return object{"@type": "error", "code": 500, "message": "cancelled"}
		}
		return object{"@type": "ok"}
	}}
	s := &Session{rpc: newRPC(f, nil)}
	defer s.rpc.close()
	if _, err := s.DownloadFile(ctx, 42); err == nil {
		t.Fatal("cancelled download succeeded")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 2 || str(f.requests[1]["@type"]) != "cancelDownloadFile" || num(f.requests[1]["file_id"]) != 42 {
		t.Fatal("native transfer continued after request cancellation")
	}
}

func TestSearchMessagesCarriesChatIdentity(t *testing.T) {
	f := &fakeTransport{recv: make(chan []byte, 10), respond: func(q object) object {
		switch str(q["@type"]) {
		case "searchMessages":
			if str(q["query"]) != "needle" || num(q["limit"]) != 5 {
				t.Error("wrong search request")
			}
			return object{"@type": "foundMessages", "messages": []any{object{"id": int64(7), "chat_id": int64(42), "date": int64(10), "content": object{"@type": "messageText", "text": object{"text": "needle"}}}}}
		case "getChat":
			return object{"id": int64(42), "title": "Chat", "type": object{"@type": "chatTypePrivate", "user_id": int64(9)}}
		default:
			t.Error("unexpected Telegram call")
			return object{"@type": "ok"}
		}
	}}
	s := &Session{rpc: newRPC(f, nil)}
	defer s.rpc.close()
	items, err := s.SearchMessages(context.Background(), "needle", 5)
	if err != nil || len(items) != 1 || items[0].Chat.TelegramChatID != 42 || items[0].Message.TelegramMessageID != 7 {
		t.Fatalf("search identity: %v %v", items, err)
	}
}
