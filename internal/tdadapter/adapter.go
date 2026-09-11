package tdadapter

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JavoxirJava/telegram-gateway/internal/sessionkey"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
)

type Reader interface {
	Read(context.Context, string, map[string]any) (json.RawMessage, error)
}
type Config struct {
	AccountID      string
	FilesDirectory string
	MaxFileBytes   int64
	// Authorize must check verified identity and current session ownership.
	// It runs for every operation; it is not a substitute for write fencing.
	Authorize func(context.Context) error
}
type Adapter struct {
	reader     Reader
	cfg        Config
	index      *Index
	files      *os.Root
	generation string
}

var _ telegram.Session = (*Adapter)(nil)
var _ telegram.Sessions = (*Adapter)(nil)

func New(reader Reader, index *Index, cfg Config) (*Adapter, error) {
	if reader == nil || index == nil || cfg.Authorize == nil || !sessionkey.ValidAccountID(cfg.AccountID) || cfg.MaxFileBytes <= 0 || cfg.MaxFileBytes > 4<<30 {
		return nil, errors.New("adapter dependencies or limits are invalid")
	}
	if !filepath.IsAbs(cfg.FilesDirectory) {
		return nil, errors.New("files directory must be absolute")
	}
	root, err := os.OpenRoot(cfg.FilesDirectory)
	if err != nil {
		return nil, errors.New("cannot open private TDLib files directory")
	}
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		root.Close()
		return nil, errors.New("cannot create adapter cursor generation")
	}
	if err := index.bind(); err != nil {
		root.Close()
		return nil, err
	}
	return &Adapter{reader: reader, cfg: cfg, index: index, files: root, generation: base64.RawURLEncoding.EncodeToString(generation[:])}, nil
}

// Close only after worker calls/download streams have stopped; not the TDLib session.
func (a *Adapter) Close() error { return a.files.Close() }
func (a *Adapter) authorized(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.cfg.Authorize(ctx)
}
func (a *Adapter) read(ctx context.Context, method string, fields map[string]any, typ string, target any) error {
	if err := a.authorized(ctx); err != nil {
		return err
	}
	raw, err := a.reader.Read(ctx, method, fields)
	if err != nil {
		return err
	}
	// A lease may have expired while the native request was in flight.
	if err := a.authorized(ctx); err != nil {
		return err
	}
	if len(raw) > 16<<20 {
		return ErrInvalidResponse
	}
	var header kindWire
	if json.Unmarshal(raw, &header) != nil || header.Type != typ {
		return ErrInvalidResponse
	}
	if json.Unmarshal(raw, target) != nil {
		return ErrInvalidResponse
	}
	return nil
}
func (a *Adapter) Profile(ctx context.Context) (telegram.Profile, error) {
	var u userWire
	if err := a.read(ctx, "getMe", nil, "user", &u); err != nil {
		return telegram.Profile{}, err
	}
	if u.ID <= 0 {
		return telegram.Profile{}, ErrInvalidResponse
	}
	return telegram.Profile{TelegramUserID: u.ID, DisplayName: strings.TrimSpace(u.FirstName + " " + u.LastName), Username: u.username()}, nil
}
func (a *Adapter) getChat(ctx context.Context, id int64) (chatWire, error) {
	if id == 0 {
		return chatWire{}, errors.New("chat identifier is required")
	}
	var c chatWire
	if err := a.read(ctx, "getChat", map[string]any{"chat_id": id}, "chat", &c); err != nil {
		return c, err
	}
	if c.ID != id {
		return chatWire{}, ErrInvalidResponse
	}
	if !c.allowed() {
		return chatWire{}, ErrExcluded
	}
	return c, nil
}

// GetChatHistory keeps source progress separate from visible messages. A page of
// excluded/unsupported messages still advances and is never mistaken for EOF.
func (a *Adapter) GetChatHistory(ctx context.Context, chatID, before int64, limit int) (telegram.HistoryPage, error) {
	if before < 0 || limit < 1 || limit > 100 {
		return telegram.HistoryPage{}, errors.New("invalid history bounds")
	}
	if _, err := a.getChat(ctx, chatID); err != nil {
		return telegram.HistoryPage{}, err
	}
	var wire struct {
		Messages []messageWire `json:"messages"`
	}
	// Ask for one extra slot when possible because the anchor may be included.
	requestLimit := limit
	if before > 0 && requestLimit < 100 {
		requestLimit++
	}
	err := a.read(ctx, "getChatHistory", map[string]any{"chat_id": chatID, "from_message_id": before, "offset": 0, "limit": requestLimit, "only_local": false}, "messages", &wire)
	if err != nil {
		return telegram.HistoryPage{}, err
	}
	if wire.Messages == nil || len(wire.Messages) > requestLimit {
		return telegram.HistoryPage{}, ErrInvalidResponse
	}
	page := telegram.HistoryPage{Items: []telegram.Message{}, SourceCount: len(wire.Messages), Exhausted: len(wire.Messages) == 0}
	for _, m := range wire.Messages {
		if m.ID <= 0 || m.ChatID != chatID || (before > 0 && m.ID > before) {
			return telegram.HistoryPage{}, ErrInvalidResponse
		}
	}
	sort.Slice(wire.Messages, func(i, j int) bool { return wire.Messages[i].ID > wire.Messages[j].ID })
	seen := make(map[int64]bool, len(wire.Messages))
	processed := 0
	for _, m := range wire.Messages {
		if m.ID == before || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		processed++
		if processed > limit {
			break
		}
		if page.NextBeforeMessageID == 0 || m.ID < page.NextBeforeMessageID {
			page.NextBeforeMessageID = m.ID
		}
		value, ok, err := decodeMessage(m)
		if err != nil {
			return telegram.HistoryPage{}, err
		}
		if ok {
			page.Items = append(page.Items, value)
		}
	}
	if !page.Exhausted && page.NextBeforeMessageID == 0 {
		// An anchor-only page is not sufficient proof of exhaustion. Never spin or
		// silently complete a sync: the queue must retry/reconcile this boundary.
		return telegram.HistoryPage{}, &Pending{}
	}
	return page, nil
}

type cursorWire struct {
	Version    int    `json:"v"`
	Generation string `json:"g"`
	Account    string `json:"a"`
	Kind       string `json:"k"`
	ChatID     int64  `json:"c,omitempty"`
	Offset     int    `json:"o,omitempty"`
	Stage      int    `json:"s,omitempty"`
	Order      int64  `json:"r,omitempty"`
	ID         int64  `json:"i,omitempty"`
}

func (a *Adapter) cursor(raw, kind string, chatID int64) (cursorWire, error) {
	c := cursorWire{Version: 1, Generation: a.generation, Account: a.cfg.AccountID, Kind: kind, ChatID: chatID}
	if raw == "" {
		return c, nil
	}
	if len(raw) > 512 {
		return c, ErrCursor
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return c, ErrCursor
	}
	c = cursorWire{}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Version != 1 || c.Generation != a.generation || c.Account != a.cfg.AccountID || c.Kind != kind || c.ChatID != chatID || c.Offset < 0 || c.Offset > 100000 || c.Stage < 0 || c.Stage > 1 || c.Order < 0 {
		return c, ErrCursor
	}
	return c, nil
}
func encodeCursor(c cursorWire) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// Get is a single-account registry for an explicitly account-routed worker.
// Never attach this registry to a consumer that handles other accounts.
func (a *Adapter) Get(ctx context.Context, accountID string) (telegram.Session, error) {
	if accountID != a.cfg.AccountID {
		return nil, errors.New("Telegram account is not owned by this adapter")
	}
	if err := a.authorized(ctx); err != nil {
		return nil, err
	}
	return a, nil
}
