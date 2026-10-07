package tdlib

import (
	"context"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
)

// SearchChats asks the server for matching chats belonging to this session;
// it does not enumerate every dialog or search unrelated public chats.
func (s *Session) SearchChats(ctx context.Context, query string, limit int) ([]telegram.Chat, error) {
	v, err := s.rpc.call(ctx, object{"@type": "searchChatsOnServer", "query": query, "limit": limit})
	if err != nil {
		return nil, err
	}
	items := []telegram.Chat{}
	for _, id := range arr(v["chat_ids"]) {
		chat, err := s.rpc.call(ctx, object{"@type": "getChat", "chat_id": num(id)})
		if err != nil {
			return nil, err
		}
		c := s.chat(chat)
		if c.Type != "secret" {
			items = append(items, c)
		}
	}
	return items, nil
}

func (s *Session) SearchMessages(ctx context.Context, query string, limit int) ([]telegram.LocatedMessage, error) {
	v, err := s.rpc.call(ctx, object{"@type": "searchMessages", "query": query, "offset": "", "limit": limit})
	if err != nil {
		return nil, err
	}
	items := []telegram.LocatedMessage{}
	for _, raw := range arr(v["messages"]) {
		message := obj(raw)
		chat, err := s.rpc.call(ctx, object{"@type": "getChat", "chat_id": num(message["chat_id"])})
		if err != nil {
			return nil, err
		}
		c := s.chat(chat)
		if c.Type != "secret" {
			items = append(items, telegram.LocatedMessage{Chat: c, Message: decodeMessage(message)})
		}
	}
	return items, nil
}
