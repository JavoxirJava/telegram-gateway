package tdlib

import (
	"context"
	"testing"
)

func TestSendTextAndScopedSearch(t *testing.T) {
	f := &fakeTransport{recv: make(chan []byte, 10), respond: func(q object) object {
		if num(q["chat_id"]) != 42 {
			t.Error("wrong destination")
		}
		switch str(q["@type"]) {
		case "sendMessage":
			content := obj(q["input_message_content"])
			if str(obj(content["text"])["text"]) != "hello" || str(content["@type"]) != "inputMessageText" {
				t.Error("wrong text payload")
			}
			return object{"id": int64(9), "@type": "message"}
		case "searchChatMessages":
			return object{"messages": []any{object{"id": int64(9), "chat_id": int64(42), "content": object{"@type": "messageText", "text": object{"text": "hello"}}}, object{"id": int64(10), "chat_id": int64(43)}}}
		default:
			t.Error("unexpected call", q)
			return object{"@type": "ok"}
		}
	}}
	s := &Session{rpc: newRPC(f, nil)}
	defer s.rpc.close()
	if id, err := s.SendText(context.Background(), 42, "hello"); err != nil || id != 9 {
		t.Fatalf("send: %d %v", id, err)
	}
	if _, err := s.SendText(context.Background(), 42, " "); err == nil {
		t.Fatal("blank send accepted")
	}
	items, err := s.SearchChatMessages(context.Background(), 42, "hello", 5)
	if err != nil || len(items) != 1 {
		t.Fatalf("scoped search: %v %v", items, err)
	}
}
