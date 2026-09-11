// Package tdadapter maps the governed, authenticated TDLib JSON reader to the
// typed worker interface. It has no authentication or raw-TDLib HTTP endpoint.
package tdadapter

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
)

var ErrInvalidResponse = errors.New("invalid TDLib adapter response")
var ErrExcluded = errors.New("chat or content excluded from the mirror")
var ErrCursor = errors.New("invalid or obsolete Telegram adapter cursor")

type Pending struct{}

func (*Pending) Error() string             { return "Telegram adapter page or download is not ready" }
func (*Pending) RetryDelay() time.Duration { return 5 * time.Second }

type kindWire struct {
	Type string `json:"@type"`
}
type chatTypeWire struct {
	Type         string `json:"@type"`
	BasicGroupID int64  `json:"basic_group_id"`
	SupergroupID int64  `json:"supergroup_id"`
	IsChannel    bool   `json:"is_channel"`
}
type positionWire struct {
	List  kindWire    `json:"list"`
	Order json.Number `json:"order"`
}
type chatWire struct {
	ID         int64          `json:"id"`
	Title      string         `json:"title"`
	Type       chatTypeWire   `json:"type"`
	Protected  bool           `json:"has_protected_content"`
	AutoDelete int64          `json:"message_auto_delete_time"`
	Positions  []positionWire `json:"positions"`
}

func (c chatWire) allowed() bool {
	if c.ID == 0 || c.Protected || c.AutoDelete > 0 {
		return false
	}
	switch c.Type.Type {
	case "chatTypePrivate", "chatTypeBasicGroup", "chatTypeSupergroup":
		return true
	}
	return false
}
func (c chatWire) model() telegram.Chat {
	kind := "private"
	switch c.Type.Type {
	case "chatTypeBasicGroup":
		kind = "group"
	case "chatTypeSupergroup":
		kind = "supergroup"
		if c.Type.IsChannel {
			kind = "channel"
		}
	}
	return telegram.Chat{TelegramChatID: c.ID, Type: kind, Title: c.Title}
}

type userWire struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Usernames struct {
		Active []string `json:"active_usernames"`
	} `json:"usernames"`
	Mutual bool `json:"is_mutual_contact"`
}

func (u userWire) username() string {
	if len(u.Usernames.Active) > 0 {
		return u.Usernames.Active[0]
	}
	return ""
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
	ReplyTo      struct {
		Type      string `json:"@type"`
		ChatID    int64  `json:"chat_id"`
		MessageID int64  `json:"message_id"`
	} `json:"reply_to"`
}
type formattedText struct {
	Text     string `json:"text"`
	Entities []any  `json:"entities"`
}
type fileWire struct {
	ID           int64 `json:"id"`
	Size         int64 `json:"size"`
	ExpectedSize int64 `json:"expected_size"`
	Remote       struct {
		UniqueID string `json:"unique_id"`
	} `json:"remote"`
	Local struct {
		Path     string `json:"path"`
		Complete bool   `json:"is_downloading_completed"`
	} `json:"local"`
}
type attachmentWire struct {
	FileName  string   `json:"file_name"`
	MIMEType  string   `json:"mime_type"`
	Document  fileWire `json:"document"`
	Video     fileWire `json:"video"`
	Audio     fileWire `json:"audio"`
	Voice     fileWire `json:"voice"`
	Animation fileWire `json:"animation"`
	Sticker   fileWire `json:"sticker"`
	Sizes     []struct {
		Photo  fileWire `json:"photo"`
		Width  int      `json:"width"`
		Height int      `json:"height"`
	} `json:"sizes"`
}

func decodeMessage(m messageWire) (telegram.Message, bool, error) {
	if m.AutoDeleteIn > 0 || m.TTL > 0 || m.Protected || (len(m.SelfDestruct) > 0 && string(m.SelfDestruct) != "null") {
		return telegram.Message{}, false, nil
	}
	var content struct {
		Type      string         `json:"@type"`
		Text      *formattedText `json:"text"`
		Caption   *formattedText `json:"caption"`
		IsSecret  bool           `json:"is_secret"`
		Photo     attachmentWire `json:"photo"`
		Video     attachmentWire `json:"video"`
		Audio     attachmentWire `json:"audio"`
		VoiceNote attachmentWire `json:"voice_note"`
		Document  attachmentWire `json:"document"`
		Animation attachmentWire `json:"animation"`
		VideoNote attachmentWire `json:"video_note"`
		Sticker   attachmentWire `json:"sticker"`
	}
	if json.Unmarshal(m.Content, &content) != nil || content.Type == "" {
		return telegram.Message{}, false, ErrInvalidResponse
	}
	if content.IsSecret {
		return telegram.Message{}, false, nil
	}
	var attachment attachmentWire
	var file fileWire
	mediaType := ""
	switch content.Type {
	case "messageText":
		if content.Text == nil {
			return telegram.Message{}, false, ErrInvalidResponse
		}
	case "messagePhoto":
		attachment = content.Photo
		mediaType = "photo"
		largest := int64(-1)
		for _, size := range attachment.Sizes {
			area := int64(size.Width) * int64(size.Height)
			if size.Width > 0 && size.Height > 0 && area > largest {
				largest = area
				file = size.Photo
			}
		}
	case "messageVideo":
		attachment = content.Video
		file = attachment.Video
		mediaType = "video"
	case "messageAudio":
		attachment = content.Audio
		file = attachment.Audio
		mediaType = "audio"
	case "messageVoiceNote":
		attachment = content.VoiceNote
		file = attachment.Voice
		mediaType = "voice_note"
	case "messageDocument":
		attachment = content.Document
		file = attachment.Document
		mediaType = "document"
	case "messageAnimation":
		attachment = content.Animation
		file = attachment.Animation
		mediaType = "animation"
	case "messageVideoNote":
		attachment = content.VideoNote
		file = attachment.Video
		mediaType = "video_note"
	case "messageSticker":
		attachment = content.Sticker
		file = attachment.Sticker
		mediaType = "sticker"
	default:
		return telegram.Message{}, false, nil
	}
	if m.Date <= 0 {
		return telegram.Message{}, false, ErrInvalidResponse
	}
	result := telegram.Message{TelegramMessageID: m.ID, Type: content.Type, SentAt: time.Unix(m.Date, 0).UTC()}
	switch m.Sender.Type {
	case "messageSenderUser":
		if m.Sender.UserID <= 0 {
			return telegram.Message{}, false, ErrInvalidResponse
		}
		result.SenderTelegramID = &m.Sender.UserID
	case "messageSenderChat":
		if m.Sender.ChatID == 0 {
			return telegram.Message{}, false, ErrInvalidResponse
		}
		result.SenderChatID = &m.Sender.ChatID
	default:
		return telegram.Message{}, false, ErrInvalidResponse
	}
	text := content.Text
	if text == nil {
		text = content.Caption
	}
	if text != nil {
		result.Content = &text.Text
		result.Entities = text.Entities
	}
	if m.EditDate > 0 {
		at := time.Unix(m.EditDate, 0).UTC()
		result.EditedAt = &at
	}
	if m.ReplyTo.Type == "messageReplyToMessage" && m.ReplyTo.MessageID > 0 && (m.ReplyTo.ChatID == 0 || m.ReplyTo.ChatID == m.ChatID) {
		result.ReplyToMessageID = &m.ReplyTo.MessageID
	}
	if mediaType != "" {
		if file.ID <= 0 {
			return telegram.Message{}, false, ErrInvalidResponse
		}
		if file.ID > 2147483647 || file.Size < 0 || file.ExpectedSize < 0 {
			return telegram.Message{}, false, ErrInvalidResponse
		}
		// Do not retain local paths, remote file credentials, thumbnails or raw JSON.
		result.Media = []telegram.Media{{Type: mediaType, TelegramFileID: file.ID, UniqueFileKey: file.Remote.UniqueID, MIMEType: attachment.MIMEType, FileName: attachment.FileName}}
		if file.Size > 0 {
			size := file.Size
			result.Media[0].FileSize = &size
		}
	}
	return result, true, nil
}
