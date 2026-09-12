// Package tglive normalizes the supported TDLib updates without retaining raw
// Telegram JSON. One Normalizer belongs to one account's ordered update loop.
package tglive

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Event struct {
	Kind         string
	ChatID       int64
	ChatType     string
	Title        string
	MessageID    int64
	SenderUserID *int64
	SenderChatID *int64
	ContentType  string
	Content      *string
	Entities     json.RawMessage
	SentAt       time.Time
	EditedAt     *time.Time
	MessageIDs   []int64
	Permanent    bool
}

type Normalizer struct {
	chats     map[int64]bool
	cloud     map[int64]bool
	protected map[int64]bool
	expiring  map[int64]bool
}

func New() *Normalizer {
	return &Normalizer{chats: make(map[int64]bool), cloud: make(map[int64]bool), protected: make(map[int64]bool), expiring: make(map[int64]bool)}
}

type messageWire struct {
	ID       int64 `json:"id"`
	ChatID   int64 `json:"chat_id"`
	Date     int64 `json:"date"`
	EditDate int64 `json:"edit_date"`
	Sender   struct {
		Type   string `json:"@type"`
		UserID int64  `json:"user_id"`
		ChatID int64  `json:"chat_id"`
	} `json:"sender_id"`
	Content      json.RawMessage `json:"content"`
	SelfDestruct json.RawMessage `json:"self_destruct_type"`
	AutoDeleteIn float64         `json:"auto_delete_in"`
	TTL          int             `json:"ttl"`
	Protected    bool            `json:"has_protected_content"`
}

func (n *Normalizer) Decode(raw []byte) (Event, bool, error) {
	var u struct {
		Type string `json:"@type"`
		Chat struct {
			ID         int64  `json:"id"`
			Title      string `json:"title"`
			Protected  bool   `json:"has_protected_content"`
			AutoDelete int64  `json:"message_auto_delete_time"`
			Type       struct {
				Type      string `json:"@type"`
				IsChannel bool   `json:"is_channel"`
			} `json:"type"`
		} `json:"chat"`
		Message    messageWire     `json:"message"`
		ChatID     int64           `json:"chat_id"`
		MessageID  int64           `json:"message_id"`
		Title      string          `json:"title"`
		Content    json.RawMessage `json:"new_content"`
		EditDate   int64           `json:"edit_date"`
		MessageIDs []int64         `json:"message_ids"`
		FromCache  bool            `json:"from_cache"`
		Permanent  bool            `json:"is_permanent"`
		Protected  bool            `json:"has_protected_content"`
		AutoDelete int64           `json:"message_auto_delete_time"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return Event{}, false, errors.New("invalid live update JSON")
	}
	e := Event{ChatID: u.ChatID, MessageID: u.MessageID}
	switch u.Type {
	case "updateNewChat":
		if u.Chat.ID == 0 {
			return Event{}, false, errors.New("invalid Telegram chat identifier")
		}
		kind := ""
		switch u.Chat.Type.Type {
		case "chatTypePrivate":
			kind = "private"
		case "chatTypeBasicGroup":
			kind = "group"
		case "chatTypeSupergroup":
			kind = "supergroup"
			if u.Chat.Type.IsChannel {
				kind = "channel"
			}
		}
		n.cloud[u.Chat.ID] = kind != ""
		n.protected[u.Chat.ID] = u.Chat.Protected
		n.expiring[u.Chat.ID] = u.Chat.AutoDelete > 0
		n.chats[u.Chat.ID] = kind != "" && !u.Chat.Protected && u.Chat.AutoDelete == 0
		if !n.chats[u.Chat.ID] {
			if kind == "" {
				return Event{}, false, nil
			}
			return Event{Kind: "blocked", ChatID: u.Chat.ID}, true, nil
		}
		return Event{Kind: "chat", ChatID: u.Chat.ID, ChatType: kind, Title: u.Chat.Title}, true, nil
	case "updateChatHasProtectedContent", "updateChatMessageAutoDeleteTime":
		if !n.cloud[u.ChatID] {
			return Event{}, false, nil
		}
		if u.Type == "updateChatHasProtectedContent" {
			n.protected[u.ChatID] = u.Protected
		} else {
			n.expiring[u.ChatID] = u.AutoDelete > 0
		}
		n.chats[u.ChatID] = !n.protected[u.ChatID] && !n.expiring[u.ChatID]
		kind := "blocked"
		if n.chats[u.ChatID] {
			kind = "allowed"
		}
		return Event{Kind: kind, ChatID: u.ChatID}, true, nil
	case "updateChatTitle":
		e.Kind = "title"
		e.Title = u.Title
	case "updateNewMessage":
		m := u.Message
		e.ChatID = m.ChatID
		e.MessageID = m.ID
		if m.Protected || m.AutoDeleteIn > 0 || m.TTL > 0 || (len(m.SelfDestruct) > 0 && string(m.SelfDestruct) != "null") {
			return Event{}, false, nil
		}
		if m.ID <= 0 || m.Date <= 0 {
			return Event{}, false, errors.New("invalid Telegram message identity")
		}
		e.Kind = "message"
		e.SentAt = time.Unix(m.Date, 0).UTC()
		if m.EditDate > 0 {
			at := time.Unix(m.EditDate, 0).UTC()
			e.EditedAt = &at
		}
		switch m.Sender.Type {
		case "messageSenderUser":
			v := m.Sender.UserID
			e.SenderUserID = &v
		case "messageSenderChat":
			v := m.Sender.ChatID
			e.SenderChatID = &v
		}
		var ok bool
		var err error
		e.ContentType, e.Content, e.Entities, ok, err = content(m.Content)
		if err != nil || !ok {
			return Event{}, false, err
		}
	case "updateMessageContent":
		e.Kind = "content"
		var ok bool
		var err error
		e.ContentType, e.Content, e.Entities, ok, err = content(u.Content)
		if err != nil {
			return Event{}, false, err
		}
		if !ok {
			// A content transition may make a previously mirrored body unavailable.
			e.Kind = "inaccessible"
			e.MessageIDs = []int64{e.MessageID}
		}
	case "updateMessageEdited":
		e.Kind = "edited"
		at := time.Unix(u.EditDate, 0).UTC()
		e.EditedAt = &at
	case "updateDeleteMessages":
		if u.FromCache {
			return Event{}, false, nil
		}
		if len(u.MessageIDs) > 100000 {
			return Event{}, false, errors.New("deletion update exceeds batch limit")
		}
		e.Kind = "deleted"
		if !u.Permanent {
			e.Kind = "inaccessible"
		}
		e.MessageIDs = u.MessageIDs
		e.Permanent = u.Permanent
		for _, id := range e.MessageIDs {
			if id <= 0 {
				return Event{}, false, errors.New("invalid deleted message identifier")
			}
		}
	default:
		return Event{}, false, nil
	}
	if !n.chats[e.ChatID] {
		return Event{}, false, nil
	}
	if e.Kind != "deleted" && e.Kind != "inaccessible" && e.Kind != "title" && e.MessageID <= 0 {
		return Event{}, false, errors.New("invalid live message identifier")
	}
	return e, true, nil
}
func content(raw json.RawMessage) (string, *string, json.RawMessage, bool, error) {
	var c struct {
		Type     string         `json:"@type"`
		Text     *formattedText `json:"text"`
		Caption  *formattedText `json:"caption"`
		IsSecret bool           `json:"is_secret"`
	}
	if json.Unmarshal(raw, &c) != nil || c.Type == "" {
		return "", nil, nil, false, errors.New("invalid message content")
	}
	if c.IsSecret || strings.Contains(c.Type, "Expired") {
		return "", nil, nil, false, nil
	}
	// Fail closed for unknown content: raw metadata can contain embedded replies,
	// credentials or expiring content. More types require an explicit mapper.
	switch c.Type {
	case "messageText", "messagePhoto", "messageVideo", "messageAudio", "messageVoiceNote", "messageDocument", "messageAnimation", "messageVideoNote", "messageSticker":
	default:
		return "", nil, nil, false, nil
	}
	text := c.Text
	if text == nil {
		text = c.Caption
	}
	entities := json.RawMessage(`[]`)
	var value *string
	if text != nil {
		v := text.Text
		value = &v
		if len(text.Entities) > 0 {
			entities = text.Entities
		}
	}
	return c.Type, value, entities, true, nil
}

type formattedText struct {
	Text     string          `json:"text"`
	Entities json.RawMessage `json:"entities"`
}
