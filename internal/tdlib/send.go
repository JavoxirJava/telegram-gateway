package tdlib

import (
	"context"
	"errors"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"strings"
	"unicode/utf8"
)

func (s *Session) SendText(ctx context.Context, chat int64, text string) (int64, error) {
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 4096 {
		return 0, errors.New("text must contain 1 to 4096 characters")
	}
	v, err := s.rpc.call(ctx, object{"@type": "sendMessage", "chat_id": chat, "input_message_content": object{"@type": "inputMessageText", "text": object{"@type": "formattedText", "text": text, "entities": []any{}}, "clear_draft": false}})
	if err != nil {
		return 0, err
	}
	id := num(v["id"])
	if id == 0 {
		return 0, errors.New("Telegram did not return a message id")
	}
	// TDLib may return a pending local message. Do not claim final delivery.
	return id, nil
}
func (s *Session) SearchChatMessages(ctx context.Context, chat int64, query string, limit int) ([]telegram.Message, error) {
	v, err := s.rpc.call(ctx, object{"@type": "searchChatMessages", "chat_id": chat, "query": query, "from_message_id": 0, "offset": 0, "limit": limit})
	if err != nil {
		return nil, err
	}
	items := []telegram.Message{}
	for _, raw := range arr(v["messages"]) {
		m := obj(raw)
		if num(m["chat_id"]) == chat {
			items = append(items, decodeMessage(m))
		}
	}
	return items, nil
}
