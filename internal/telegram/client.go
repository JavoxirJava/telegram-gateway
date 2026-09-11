package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

type Profile struct {
	TelegramUserID int64
	DisplayName    string
	Username       string
}

type Chat struct {
	TelegramChatID int64
	Type           string
	Title          string
	Username       string
	Metadata       map[string]any
	LastMessageID  *int64
	LastMessageAt  *time.Time
}

type ChatPage struct {
	Items      []Chat
	NextCursor string
}

type Message struct {
	TelegramMessageID int64
	SenderTelegramID  *int64
	SenderChatID      *int64
	Type              string
	Content           *string
	Entities          []any
	ReplyToMessageID  *int64
	ForwardInfo       map[string]any
	Metadata          map[string]any
	SentAt            time.Time
	EditedAt          *time.Time
	Media             []Media
}

type Media struct {
	Type           string
	TelegramFileID int64
	UniqueFileKey  string
	MIMEType       string
	FileName       string
	FileSize       *int64
}

type Contact struct {
	TelegramUserID int64
	FirstName      string
	LastName       string
	Username       string
	PhoneHash      []byte
	IsMutual       bool
	Metadata       map[string]any
}

type Member struct {
	PeerType       string
	TelegramPeerID int64
	FirstName      string
	LastName       string
	Username       string
	Role           string
	Metadata       map[string]any
}

type MemberPage struct {
	Items      []Member
	NextCursor string
}

type Download struct {
	Reader      io.ReadCloser
	Size        int64
	ContentType string
	FileName    string
}

type Session interface {
	Profile(ctx context.Context) (Profile, error)
	ListChats(ctx context.Context, cursor string, limit int) (ChatPage, error)
	GetChatHistory(ctx context.Context, telegramChatID, beforeMessageID int64, limit int) ([]Message, error)
	ListContacts(ctx context.Context) ([]Contact, error)
	ListMembers(ctx context.Context, telegramChatID int64, cursor string, limit int) (MemberPage, error)
	DownloadFile(ctx context.Context, telegramFileID int64) (Download, error)
}

type Sessions interface {
	Get(ctx context.Context, accountID string) (Session, error)
}

type FloodWaitError struct {
	RetryAfter time.Duration
	Cause      error
}

func (e *FloodWaitError) Error() string {
	if e == nil {
		return "Telegram flood wait"
	}
	if e.Cause != nil {
		return fmt.Sprintf("Telegram flood wait for %s: %v", e.RetryAfter, e.Cause)
	}
	return fmt.Sprintf("Telegram flood wait for %s", e.RetryAfter)
}

func (e *FloodWaitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func AsFloodWait(err error) (time.Duration, bool) {
	var flood *FloodWaitError
	if !errors.As(err, &flood) || flood.RetryAfter <= 0 {
		return 0, false
	}
	return flood.RetryAfter, true
}
