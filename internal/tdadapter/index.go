package tdadapter

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"

	"github.com/JavoxirJava/telegram-gateway/internal/tdjson"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
)

const maxIndexEntries = 100000

// Index must receive EVERY ordered update before the live persistence callback.
// It stores only minimal chat/user metadata, never message bodies or phone data.
// A fresh native session/reconnect rebuild requires a fresh Index.
type Index struct {
	mu     sync.RWMutex
	bound  bool
	chats  map[int64]chatWire
	orders [2]map[int64]int64
	users  map[int64]userWire
	loaded [2]bool
}

func NewIndex() *Index {
	return &Index{chats: map[int64]chatWire{}, users: map[int64]userWire{}, orders: [2]map[int64]int64{{}, {}}}
}

// One native update stream and one adapter may own an index. Sharing it across
// accounts would leak cached metadata even though individual RPCs are guarded.
func (i *Index) bind() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.bound {
		return errors.New("TDLib index is already bound")
	}
	i.bound = true
	return nil
}

func listStage(kind string) int {
	switch kind {
	case "chatListMain":
		return 0
	case "chatListArchive":
		return 1
	}
	return -1
}
func (i *Index) Observe(_ context.Context, raw json.RawMessage) error {
	var u struct {
		Type       string         `json:"@type"`
		Chat       chatWire       `json:"chat"`
		User       userWire       `json:"user"`
		ChatID     int64          `json:"chat_id"`
		Title      string         `json:"title"`
		Protected  bool           `json:"has_protected_content"`
		AutoDelete int64          `json:"message_auto_delete_time"`
		Position   positionWire   `json:"position"`
		Positions  []positionWire `json:"positions"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return ErrInvalidResponse
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	switch u.Type {
	case "updateNewChat":
		if u.Chat.ID == 0 {
			return ErrInvalidResponse
		}
		if _, ok := i.chats[u.Chat.ID]; !ok && len(i.chats) >= maxIndexEntries {
			return errors.New("chat index capacity reached")
		}
		i.chats[u.Chat.ID] = u.Chat
		return i.positions(u.Chat.ID, u.Chat.Positions, true)
	case "updateUser":
		if u.User.ID <= 0 {
			return ErrInvalidResponse
		}
		if _, ok := i.users[u.User.ID]; !ok && len(i.users) >= maxIndexEntries {
			return errors.New("user index capacity reached")
		}
		i.users[u.User.ID] = u.User
	case "updateChatPosition":
		return i.positions(u.ChatID, []positionWire{u.Position}, false)
	case "updateChatLastMessage", "updateChatDraftMessage":
		return i.positions(u.ChatID, u.Positions, true)
	case "updateChatTitle", "updateChatHasProtectedContent", "updateChatMessageAutoDeleteTime":
		c, ok := i.chats[u.ChatID]
		if !ok {
			return nil
		}
		switch u.Type {
		case "updateChatTitle":
			c.Title = u.Title
		case "updateChatHasProtectedContent":
			c.Protected = u.Protected
		case "updateChatMessageAutoDeleteTime":
			c.AutoDelete = u.AutoDelete
		}
		i.chats[u.ChatID] = c
	}
	return nil
}
func (i *Index) positions(id int64, positions []positionWire, replace bool) error {
	if id == 0 {
		return ErrInvalidResponse
	}
	if _, ok := i.chats[id]; !ok {
		return nil
	} // Native sends newChat first; never invent one.
	if replace {
		for s := range i.orders {
			delete(i.orders[s], id)
		}
	}
	for _, p := range positions {
		stage := listStage(p.List.Type)
		if stage < 0 {
			continue
		}
		order, err := p.Order.Int64()
		if err != nil || order < 0 {
			return ErrInvalidResponse
		}
		if order == 0 {
			delete(i.orders[stage], id)
		} else {
			i.orders[stage][id] = order
		}
	}
	return nil
}
func (i *Index) user(id int64) (userWire, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	u, ok := i.users[id]
	return u, ok
}

type indexedChat struct {
	chat  chatWire
	order int64
}

func (i *Index) page(stage int, before cursorWire, limit int) ([]indexedChat, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]indexedChat, 0)
	for id, order := range i.orders[stage] {
		c := i.chats[id]
		if !c.allowed() {
			continue
		}
		if before.Order > 0 && (order > before.Order || (order == before.Order && id >= before.ID)) {
			continue
		}
		out = append(out, indexedChat{chat: c, order: order})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].order == out[b].order {
			return out[a].chat.ID > out[b].chat.ID
		}
		return out[a].order > out[b].order
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, i.loaded[stage]
}

// ListChats uses TDLib's update-driven order, not getChats' informational prefix.
// Keyset cursors are live scans, NOT immutable snapshots. Reconcile on restart
// or concurrent chat moves; a folder removal must not mean deleting chat history.
func (a *Adapter) ListChats(ctx context.Context, raw string, limit int) (telegram.ChatPage, error) {
	if limit < 1 || limit > 100 {
		return telegram.ChatPage{}, errors.New("invalid chat page size")
	}
	if err := a.authorized(ctx); err != nil {
		return telegram.ChatPage{}, err
	}
	cursor, err := a.cursor(raw, "chats", 0)
	if err != nil {
		return telegram.ChatPage{}, err
	}
	// Bound work per invocation. The queue retries Pending instead of busy-looping.
	for attempts := 0; attempts < 4; attempts++ {
		rows, loaded := a.index.page(cursor.Stage, cursor, limit)
		if len(rows) > 0 {
			page := telegram.ChatPage{Items: make([]telegram.Chat, 0, len(rows))}
			for _, row := range rows {
				page.Items = append(page.Items, row.chat.model())
			}
			last := rows[len(rows)-1]
			cursor.Order = last.order
			cursor.ID = last.chat.ID
			page.NextCursor = encodeCursor(cursor)
			return page, nil
		}
		if loaded {
			if cursor.Stage == 1 {
				return telegram.ChatPage{Items: []telegram.Chat{}}, nil
			}
			cursor.Stage = 1
			cursor.Order = 0
			cursor.ID = 0
			continue
		}
		list := "chatListMain"
		if cursor.Stage == 1 {
			list = "chatListArchive"
		}
		err := a.read(ctx, "loadChats", map[string]any{"chat_list": map[string]any{"@type": list}, "limit": limit}, "ok", new(struct{}))
		if err != nil {
			var tdErr *tdjson.Error
			if !errors.As(err, &tdErr) || tdErr.Code != 404 {
				return telegram.ChatPage{}, err
			}
			a.index.mu.Lock()
			a.index.loaded[cursor.Stage] = true
			a.index.mu.Unlock()
		}
	}
	return telegram.ChatPage{}, &Pending{}
}
