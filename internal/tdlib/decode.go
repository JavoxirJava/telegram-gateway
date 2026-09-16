package tdlib

import (
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"strings"
	"time"
)

func decodeMessage(v object) telegram.Message {
	c := obj(v["content"])
	m := telegram.Message{TelegramMessageID: num(v["id"]), Type: strings.TrimPrefix(str(c["@type"]), "message"), SentAt: time.Unix(num(v["date"]), 0).UTC(), Metadata: object{"is_outgoing": yes(v["is_outgoing"]), "media_album_id": v["media_album_id"], "content": sanitizedContent(c)}}
	if m.Type == "" {
		m.Type = "Unknown"
	}
	sender := obj(v["sender_id"])
	switch str(sender["@type"]) {
	case "messageSenderUser":
		id := num(sender["user_id"])
		m.SenderTelegramID = &id
	case "messageSenderChat":
		id := num(sender["chat_id"])
		m.SenderChatID = &id
	}
	if edit := num(v["edit_date"]); edit > 0 {
		t := time.Unix(edit, 0).UTC()
		m.EditedAt = &t
	}
	reply := obj(v["reply_to"])
	if id := num(reply["message_id"]); id != 0 {
		m.ReplyToMessageID = &id
	}
	if f := obj(v["forward_info"]); len(f) > 0 {
		m.ForwardInfo = f
	}
	m.Content, m.Entities, m.Media = decodeContent(c)
	return m
}
func decodeContent(c object) (*string, []any, []telegram.Media) {
	formatted := obj(c["text"])
	if len(formatted) == 0 {
		formatted = obj(c["caption"])
	}
	var text *string
	if t, ok := formatted["text"].(string); ok {
		text = &t
	}
	media := []telegram.Media{}
	kind := str(c["@type"])
	switch kind {
	case "messagePhoto":
		var best object
		for _, raw := range arr(obj(c["photo"])["sizes"]) {
			p := obj(raw)
			if num(p["width"])*num(p["height"]) > num(best["width"])*num(best["height"]) {
				best = p
			}
		}
		if len(best) > 0 {
			media = append(media, decodeFile(obj(best["photo"]), "photo", "image/jpeg", "photo.jpg"))
		}
	case "messageVideo", "messageDocument", "messageAudio", "messageVoiceNote", "messageVideoNote", "messageAnimation", "messageSticker":
		key := strings.TrimPrefix(kind, "message")
		key = strings.ToLower(key[:1]) + key[1:]
		// TDLib JSON field names use snake_case for voice/video notes.
		switch key {
		case "voiceNote":
			key = "voice_note"
		case "videoNote":
			key = "video_note"
		}
		item := obj(c[key])
		file := obj(item[key])
		if key == "voice_note" {
			file = obj(item["voice"])
		}
		if key == "video_note" {
			file = obj(item["video"])
		}
		mime := str(item["mime_type"])
		if mime == "" {
			mime = "application/octet-stream"
		}
		if len(file) > 0 {
			media = append(media, decodeFile(file, key, mime, str(item["file_name"])))
		}
	}
	return text, arr(formatted["entities"]), media
}
func decodeFile(v object, kind, mime, name string) telegram.Media {
	size := num(v["size"])
	return telegram.Media{Type: kind, TelegramFileID: num(v["id"]), UniqueFileKey: str(obj(v["remote"])["unique_id"]), MIMEType: mime, FileName: name, FileSize: &size}
}

// Event is the subset of Telegram updates needed to maintain the stored mirror.
// It contains no authorization codes, API credentials, or session material.
type Event struct {
	Kind           string
	TelegramChatID int64
	Chat           *telegram.Chat
	Message        *telegram.Message
	MessageID      int64
	MessageIDs     []int64
	Content        *string
	Entities       []any
	Media          []telegram.Media
	EditedAt       *time.Time
	Permanent      bool
}

func (s *Session) Event(v object) Event {
	e := Event{Kind: str(v["@type"]), TelegramChatID: num(v["chat_id"]), MessageID: num(v["message_id"])}
	switch e.Kind {
	case "updateNewChat":
		c := s.chat(obj(v["chat"]))
		e.Chat = &c
		e.TelegramChatID = c.TelegramChatID
	case "updateNewMessage":
		m := obj(v["message"])
		e.TelegramChatID = num(m["chat_id"])
		msg := decodeMessage(m)
		e.Message = &msg
	case "updateMessageContent":
		e.Content, e.Entities, e.Media = decodeContent(obj(v["new_content"]))
	case "updateMessageEdited":
		t := time.Unix(num(v["edit_date"]), 0).UTC()
		e.EditedAt = &t
	case "updateDeleteMessages":
		e.Permanent = yes(v["is_permanent"]) && !yes(v["from_cache"])
		for _, id := range arr(v["message_ids"]) {
			e.MessageIDs = append(e.MessageIDs, num(id))
		}
	}
	if e.Chat == nil {
		if c, ok := s.cachedChat(e.TelegramChatID); ok {
			chat := s.chat(c)
			e.Chat = &chat
		}
	}
	return e
}

// Preserve rich message content without leaking local paths or reusable remote
// file handles. Contact-card phone numbers follow the gateway's privacy policy.
func sanitizedContent(v any) any {
	switch value := v.(type) {
	case map[string]any:
		if str(value["@type"]) == "file" {
			return object{"@type": "file", "size": value["size"], "unique_id": obj(value["remote"])["unique_id"]}
		}
		out := object{}
		for k, item := range value {
			if k != "local" && k != "phone_number" {
				out[k] = sanitizedContent(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = sanitizedContent(item)
		}
		return out
	default:
		return value
	}
}
