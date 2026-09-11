package syncjob

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const SchemaVersion = 1

type Kind string

const (
	KindAccountBootstrap Kind = "account.bootstrap"
	KindContactsSync     Kind = "contacts.sync"
	KindChatHistory      Kind = "chat.history"
	KindChatMembers      Kind = "chat.members"
	KindMediaDownload    Kind = "media.download"
)

const (
	SubjectAccountBootstrap = "telegram.sync.account"
	SubjectContactsSync     = "telegram.sync.contacts"
	SubjectChatHistory      = "telegram.sync.chat"
	SubjectChatMembers      = "telegram.sync.members"
	SubjectMediaDownload    = "telegram.media.download"
)

type Envelope struct {
	Version    int             `json:"version"`
	JobID      string          `json:"job_id"`
	Kind       Kind            `json:"kind"`
	AccountID  string          `json:"account_id"`
	DedupKey   string          `json:"dedup_key"`
	EnqueuedAt time.Time       `json:"enqueued_at"`
	Payload    json.RawMessage `json:"payload"`
}

type AccountBootstrapPayload struct {
	Force  bool   `json:"force"`
	Cursor string `json:"cursor,omitempty"`
}

type ContactsSyncPayload struct{}

type ChatHistoryPayload struct {
	ChatID            string `json:"chat_id"`
	TelegramChatID    int64  `json:"telegram_chat_id"`
	BeforeMessageID   int64  `json:"before_message_id,omitempty"`
	RequestedPageSize int    `json:"requested_page_size"`
}

type ChatMembersPayload struct {
	ChatID         string `json:"chat_id"`
	TelegramChatID int64  `json:"telegram_chat_id"`
	Cursor         string `json:"cursor,omitempty"`
	Limit          int    `json:"limit"`
}

type MediaDownloadPayload struct {
	MediaID        string `json:"media_id"`
	MessageID      string `json:"message_id"`
	TelegramFileID int64  `json:"telegram_file_id"`
}

func NewEnvelope(kind Kind, accountID, dedupKey string, payload any) (Envelope, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return Envelope{}, errors.New("account id is required")
	}
	if !kind.Valid() {
		return Envelope{}, fmt.Errorf("unsupported sync job kind %q", kind)
	}

	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal sync job payload: %w", err)
	}
	jobID, err := randomID()
	if err != nil {
		return Envelope{}, err
	}
	if strings.TrimSpace(dedupKey) == "" {
		dedupKey = jobID
	}

	return Envelope{
		Version:    SchemaVersion,
		JobID:      jobID,
		Kind:       kind,
		AccountID:  accountID,
		DedupKey:   dedupKey,
		EnqueuedAt: time.Now().UTC(),
		Payload:    encodedPayload,
	}, nil
}

func (e Envelope) Validate() error {
	if e.Version != SchemaVersion {
		return fmt.Errorf("unsupported sync job schema version %d", e.Version)
	}
	if strings.TrimSpace(e.JobID) == "" {
		return errors.New("job id is required")
	}
	if !e.Kind.Valid() {
		return fmt.Errorf("unsupported sync job kind %q", e.Kind)
	}
	if strings.TrimSpace(e.AccountID) == "" {
		return errors.New("account id is required")
	}
	if len(e.Payload) == 0 {
		return errors.New("payload is required")
	}
	return nil
}

func (k Kind) Valid() bool {
	switch k {
	case KindAccountBootstrap, KindContactsSync, KindChatHistory, KindChatMembers, KindMediaDownload:
		return true
	default:
		return false
	}
}

func SubjectFor(kind Kind) (string, error) {
	switch kind {
	case KindAccountBootstrap:
		return SubjectAccountBootstrap, nil
	case KindContactsSync:
		return SubjectContactsSync, nil
	case KindChatHistory:
		return SubjectChatHistory, nil
	case KindChatMembers:
		return SubjectChatMembers, nil
	case KindMediaDownload:
		return SubjectMediaDownload, nil
	default:
		return "", fmt.Errorf("unsupported sync job kind %q", kind)
	}
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate sync job id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
