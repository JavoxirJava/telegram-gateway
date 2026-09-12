package tdadapter

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
)

// Message re-resolves native file identifiers from an authorized message. A file
// ID from an older TDLib database is never trusted for a new download.
func (a *Adapter) Message(ctx context.Context, chatID, messageID int64) (telegram.Message, error) {
	if messageID <= 0 {
		return telegram.Message{}, errors.New("invalid message id")
	}
	if _, err := a.getChat(ctx, chatID); err != nil {
		return telegram.Message{}, err
	}
	var m messageWire
	if err := a.read(ctx, "getMessage", map[string]any{"chat_id": chatID, "message_id": messageID}, "message", &m); err != nil {
		return telegram.Message{}, err
	}
	if m.ID != messageID || m.ChatID != chatID {
		return telegram.Message{}, ErrInvalidResponse
	}
	result, ok, err := decodeMessage(m)
	if err != nil {
		return telegram.Message{}, err
	}
	if !ok {
		return telegram.Message{}, ErrExcluded
	}
	return result, nil
}

// Messages never interprets unavailability as proof of permanent deletion.
// Missing/excluded items are reported separately for reversible access blocking.
func (a *Adapter) Messages(ctx context.Context, chatID int64, ids []int64) (map[int64]telegram.Message, []int64, error) {
	if len(ids) < 1 || len(ids) > 100 {
		return nil, nil, errors.New("message batch must contain 1..100 ids")
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return nil, nil, errors.New("invalid message batch")
		}
		seen[id] = true
	}
	if _, err := a.getChat(ctx, chatID); err != nil {
		return nil, nil, err
	}
	var batch struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := a.read(ctx, "getMessages", map[string]any{"chat_id": chatID, "message_ids": ids}, "messages", &batch); err != nil {
		return nil, nil, err
	}
	if len(batch.Messages) != len(ids) {
		return nil, nil, ErrInvalidResponse
	}
	out := map[int64]telegram.Message{}
	var unavailable []int64
	for i, raw := range batch.Messages {
		if string(raw) == "null" {
			unavailable = append(unavailable, ids[i])
			continue
		}
		var m messageWire
		if json.Unmarshal(raw, &m) != nil || m.ID != ids[i] || m.ChatID != chatID {
			return nil, nil, ErrInvalidResponse
		}
		v, ok, err := decodeMessage(m)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			out[m.ID] = v
		} else {
			unavailable = append(unavailable, m.ID)
		}
	}
	return out, unavailable, nil
}
