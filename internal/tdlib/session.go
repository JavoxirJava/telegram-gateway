package tdlib

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
)

type Options struct {
	AccountID string
	APIID     int64
	APIHash   string
	MasterKey []byte
	Directory string
	OnUpdate  func(Event) error
}
type Session struct {
	rpc       *rpc
	mu        sync.RWMutex
	state     object
	users     map[int64]object
	chats     map[int64]object
	groups    map[int64]object
	options   Options
	ctx       context.Context
	cancel    context.CancelFunc
	ready     chan struct{}
	readyOnce sync.Once
	closeOnce sync.Once
	errorText string
}

func Open(ctx context.Context, o Options) (*Session, error) {
	if o.APIID <= 0 || o.APIHash == "" || len(o.MasterKey) != 32 {
		return nil, errors.New("Telegram API ID, API hash and a 32-byte session key are required")
	}
	if o.AccountID == "" || filepath.Base(o.AccountID) != o.AccountID {
		return nil, errors.New("invalid account ID")
	}
	dir := filepath.Join(o.Directory, o.AccountID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	t, err := newTransport()
	if err != nil {
		return nil, err
	}
	run, cancel := context.WithCancel(ctx)
	s := &Session{options: o, ctx: run, cancel: cancel, state: object{"@type": "starting"}, users: map[int64]object{}, chats: map[int64]object{}, groups: map[int64]object{}, ready: make(chan struct{})}
	// The receiver is started only after the session has a usable RPC pointer.
	r := &rpc{transport: t, pending: make(map[string]chan object), stop: make(chan struct{}), done: make(chan struct{}), update: s.update}
	s.rpc = r
	go r.receive()
	go func() { <-run.Done(); r.close() }()
	return s, nil
}
func (s *Session) update(v object) {
	kind := str(v["@type"])
	s.mu.Lock()
	switch kind {
	case "updateUser":
		u := obj(v["user"])
		s.users[num(u["id"])] = u
	case "updateNewChat":
		c := obj(v["chat"])
		s.chats[num(c["id"])] = c
	case "updateSupergroup":
		g := obj(v["supergroup"])
		s.groups[num(g["id"])] = g
	case "updateChatTitle", "updateChatLastMessage", "updateChatPosition":
		id := num(v["chat_id"])
		old := s.chats[id]
		c := object{}
		for k, val := range old {
			c[k] = val
		}
		if kind == "updateChatTitle" {
			c["title"] = v["title"]
		}
		if kind == "updateChatLastMessage" {
			c["last_message"] = v["last_message"]
		}
		s.chats[id] = c
	case "updateAuthorizationState":
		s.state = obj(v["authorization_state"])
	}
	state := str(s.state["@type"])
	s.mu.Unlock()
	if kind == "updateAuthorizationState" {
		switch state {
		case "authorizationStateWaitTdlibParameters":
			go s.initialize()
		case "authorizationStateReady":
			s.readyOnce.Do(func() { close(s.ready) })
		case "authorizationStateClosed":
			s.cancel()
		}
	}
	if s.options.OnUpdate != nil && isDataUpdate(kind) {
		if err := s.options.OnUpdate(s.Event(v)); err != nil {
			s.mu.Lock()
			s.errorText = "failed to persist Telegram update"
			s.mu.Unlock()
			s.cancel()
		}
	}
}
func isDataUpdate(kind string) bool {
	switch kind {
	case "updateNewMessage", "updateMessageContent", "updateMessageEdited", "updateDeleteMessages", "updateNewChat", "updateChatTitle", "updateChatLastMessage":
		return true
	}
	return false
}
func (s *Session) initialize() {
	mac := hmac.New(sha256.New, s.options.MasterKey)
	mac.Write([]byte("telegram-gateway/tdlib/" + s.options.AccountID))
	_, err := s.rpc.call(s.ctx, object{"@type": "setTdlibParameters", "api_id": s.options.APIID, "api_hash": s.options.APIHash,
		"database_directory":      filepath.Join(s.options.Directory, s.options.AccountID, "database"),
		"files_directory":         filepath.Join(s.options.Directory, s.options.AccountID, "files"),
		"database_encryption_key": base64.StdEncoding.EncodeToString(mac.Sum(nil)),
		"use_file_database":       true, "use_chat_info_database": true, "use_message_database": true, "use_secret_chats": false,
		"system_language_code": "en", "device_model": "Telegram Gateway", "system_version": "Linux", "application_version": "1.0.0"})
	if err != nil {
		s.mu.Lock()
		s.errorText = err.Error()
		s.mu.Unlock()
	}
}
func (s *Session) State() object {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := object{"state": str(s.state["@type"]), "error": s.errorText}
	for _, k := range []string{"password_hint", "link", "code_info", "recovery_email_address_pattern"} {
		if v, ok := s.state[k]; ok {
			result[k] = v
		}
	}
	return result
}
func (s *Session) Authorize(ctx context.Context, action, value string) error {
	var q object
	switch action {
	case "phone":
		q = object{"@type": "setAuthenticationPhoneNumber", "phone_number": value}
	case "code":
		q = object{"@type": "checkAuthenticationCode", "code": value}
	case "password":
		q = object{"@type": "checkAuthenticationPassword", "password": value}
	case "email":
		q = object{"@type": "setAuthenticationEmailAddress", "email_address": value}
	case "email_code":
		q = object{"@type": "checkAuthenticationEmailCode", "code": object{"@type": "emailAddressAuthenticationCode", "code": value}}
	case "qr":
		q = object{"@type": "requestQrCodeAuthentication", "other_user_ids": []int64{}}
	default:
		return errors.New("unsupported authentication action")
	}
	_, err := s.rpc.call(ctx, q)
	return err
}
func (s *Session) IsReady() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return str(s.state["@type"]) == "authorizationStateReady"
}
func (s *Session) Done() <-chan struct{} { return s.ctx.Done() }
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.rpc.call(ctx, object{"@type": "close"})
		s.cancel()
		s.rpc.close()
	})
}
func (s *Session) Profile(ctx context.Context) (telegram.Profile, error) {
	v, err := s.rpc.call(ctx, object{"@type": "getMe"})
	if err != nil {
		return telegram.Profile{}, err
	}
	return telegram.Profile{TelegramUserID: num(v["id"]), DisplayName: str(v["first_name"]) + " " + str(v["last_name"]), Username: username(v)}, nil
}
func (s *Session) cachedChat(id int64) (object, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.chats[id]
	return c, ok
}
func (s *Session) cachedUser(id int64) object { s.mu.RLock(); defer s.mu.RUnlock(); return s.users[id] }

// Cursor encodes the loaded offset for main and archive chat lists. Each page
// loads more chats and returns cached objects delivered before the RPC response.
func (s *Session) ListChats(ctx context.Context, cursor string, limit int) (telegram.ChatPage, error) {
	list, offset := 0, 0
	if cursor != "" {
		if _, err := fmt.Sscanf(cursor, "%d:%d", &list, &offset); err != nil || list < 0 || list > 1 || offset < 0 {
			return telegram.ChatPage{}, errors.New("invalid chat cursor")
		}
	}
	if limit < 1 || limit > 100 {
		limit = 100
	}
	listType := "chatListMain"
	if list == 1 {
		listType = "chatListArchive"
	}
	l := object{"@type": listType}
	_, loadErr := s.rpc.call(ctx, object{"@type": "loadChats", "chat_list": l, "limit": limit})
	exhausted := false
	if loadErr != nil {
		var e *Error
		if errors.As(loadErr, &e) && e.Code == 404 {
			exhausted = true
		} else {
			return telegram.ChatPage{}, loadErr
		}
	}
	v, err := s.rpc.call(ctx, object{"@type": "getChats", "chat_list": l, "limit": offset + limit})
	if err != nil {
		return telegram.ChatPage{}, err
	}
	ids := arr(v["chat_ids"])
	page := telegram.ChatPage{Items: []telegram.Chat{}}
	end := len(ids)
	if end > offset+limit {
		end = offset + limit
	}
	for i := offset; i < end; i++ {
		if c, ok := s.cachedChat(num(ids[i])); ok {
			if str(obj(c["type"])["@type"]) != "chatTypeSecret" {
				page.Items = append(page.Items, s.chat(c))
			}
		}
	}
	if !exhausted || end < len(ids) || end-offset == limit {
		page.NextCursor = strconv.Itoa(list) + ":" + strconv.Itoa(end)
	} else if list == 0 {
		page.NextCursor = "1:0"
	}
	if end == offset && !exhausted {
		return telegram.ChatPage{}, errors.New("Telegram chat list has not loaded yet")
	}
	return page, nil
}
func (s *Session) chat(v object) telegram.Chat {
	t := obj(v["type"])
	kind := "private"
	name := ""
	switch str(t["@type"]) {
	case "chatTypeSecret":
		kind = "secret"
	case "chatTypeBasicGroup":
		kind = "group"
	case "chatTypeSupergroup":
		kind = "supergroup"
		if yes(t["is_channel"]) {
			kind = "channel"
		}
		s.mu.RLock()
		name = username(s.groups[num(t["supergroup_id"])])
		s.mu.RUnlock()
	case "chatTypePrivate":
		name = username(s.cachedUser(num(t["user_id"])))
	}
	c := telegram.Chat{TelegramChatID: num(v["id"]), Type: kind, Title: str(v["title"]), Username: name, Metadata: object{"type": t}}
	last := obj(v["last_message"])
	if id := num(last["id"]); id != 0 {
		at := time.Unix(num(last["date"]), 0).UTC()
		c.LastMessageID = &id
		c.LastMessageAt = &at
	}
	return c
}
func (s *Session) GetChatHistory(ctx context.Context, chatID, before int64, limit int) ([]telegram.Message, error) {
	offset := 0
	v, err := s.rpc.call(ctx, object{"@type": "getChatHistory", "chat_id": chatID, "from_message_id": before, "offset": offset, "limit": limit, "only_local": false})
	if err != nil {
		return nil, err
	}
	out := []telegram.Message{}
	for _, raw := range arr(v["messages"]) {
		m := obj(raw)
		id := num(m["id"])
		if before != 0 && id >= before {
			continue
		}
		out = append(out, decodeMessage(m))
	}
	return out, nil
}
func (s *Session) ListContacts(ctx context.Context) ([]telegram.Contact, error) {
	v, err := s.rpc.call(ctx, object{"@type": "getContacts"})
	if err != nil {
		return nil, err
	}
	out := []telegram.Contact{}
	for _, id := range arr(v["user_ids"]) {
		u := s.cachedUser(num(id))
		if u == nil {
			return nil, errors.New("Telegram contact cache incomplete")
		}
		mac := hmac.New(sha256.New, s.options.MasterKey)
		mac.Write([]byte(str(u["phone_number"])))
		out = append(out, telegram.Contact{TelegramUserID: num(id), FirstName: str(u["first_name"]), LastName: str(u["last_name"]), Username: username(u), PhoneHash: mac.Sum(nil), IsMutual: yes(u["is_mutual_contact"])})
	}
	return out, nil
}
func (s *Session) ListMembers(ctx context.Context, chatID int64, cursor string, limit int) (telegram.MemberPage, error) {
	offset := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			return telegram.MemberPage{}, errors.New("invalid member cursor")
		}
		offset = n
	}
	c, ok := s.cachedChat(chatID)
	if !ok {
		// Durable member jobs may resume before the chat-list bootstrap after a
		// restart. Load their chat directly instead of waiting for that queued job.
		var err error
		c, err = s.rpc.call(ctx, object{"@type": "getChat", "chat_id": chatID})
		if err != nil {
			return telegram.MemberPage{}, err
		}
	}
	t := obj(c["type"])
	var v object
	var err error
	switch str(t["@type"]) {
	case "chatTypeBasicGroup":
		v, err = s.rpc.call(ctx, object{"@type": "getBasicGroupFullInfo", "basic_group_id": num(t["basic_group_id"])})
	case "chatTypeSupergroup":
		v, err = s.rpc.call(ctx, object{"@type": "getSupergroupMembers", "supergroup_id": num(t["supergroup_id"]), "filter": object{"@type": "supergroupMembersFilterRecent"}, "offset": offset, "limit": limit})
	default:
		return telegram.MemberPage{}, errors.New("this chat has no member list")
	}
	if err != nil {
		return telegram.MemberPage{}, err
	}
	members := arr(v["members"])
	page := telegram.MemberPage{Items: []telegram.Member{}}
	for _, raw := range members {
		m := obj(raw)
		sender := obj(m["member_id"])
		id := num(sender["user_id"])
		kind := "user"
		u := s.cachedUser(id)
		if str(sender["@type"]) == "messageSenderChat" {
			id = num(sender["chat_id"])
			kind = "chat"
			u, _ = s.cachedChat(id)
		}
		page.Items = append(page.Items, telegram.Member{PeerType: kind, TelegramPeerID: id, FirstName: str(u["first_name"]), LastName: str(u["last_name"]), Username: username(u), Role: str(obj(m["status"])["@type"])})
	}
	if str(t["@type"]) == "chatTypeSupergroup" && len(members) > 0 && int64(offset+len(members)) < num(v["total_count"]) {
		page.NextCursor = strconv.Itoa(offset + len(members))
	}
	return page, nil
}
func (s *Session) ResolveMessageFile(ctx context.Context, chatID, messageID int64, uniqueFileKey, mediaType string) (int64, error) {
	if _, ok := s.cachedChat(chatID); !ok {
		if _, err := s.rpc.call(ctx, object{"@type": "getChat", "chat_id": chatID}); err != nil {
			return 0, err
		}
	}
	v, err := s.rpc.call(ctx, object{"@type": "getMessage", "chat_id": chatID, "message_id": messageID})
	if err != nil {
		return 0, err
	}
	for _, attachment := range decodeMessage(v).Media {
		if attachment.Type == mediaType && (uniqueFileKey == "" || attachment.UniqueFileKey == uniqueFileKey) && attachment.TelegramFileID != 0 {
			return attachment.TelegramFileID, nil
		}
	}
	return 0, &Error{Code: 404, Message: "message attachment is unavailable"}
}

func (s *Session) DownloadFile(ctx context.Context, id int64) (telegram.Download, error) {
	v, err := s.rpc.call(ctx, object{"@type": "downloadFile", "file_id": id, "priority": 1, "offset": 0, "limit": 0, "synchronous": true})
	if err != nil {
		return telegram.Download{}, err
	}
	local := obj(v["local"])
	if !yes(local["is_downloading_completed"]) {
		return telegram.Download{}, errors.New("Telegram file download incomplete")
	}
	path := str(local["path"])
	base, err := filepath.Abs(filepath.Join(s.options.Directory, s.options.AccountID, "files"))
	if err != nil {
		return telegram.Download{}, err
	}
	actual, err := filepath.EvalSymlinks(path)
	if err != nil {
		return telegram.Download{}, err
	}
	rel, err := filepath.Rel(base, actual)
	if err != nil || rel == ".." || len(rel) > 3 && rel[:3] == "../" {
		return telegram.Download{}, errors.New("Telegram file escaped session directory")
	}
	f, err := os.Open(actual)
	if err != nil {
		return telegram.Download{}, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return telegram.Download{}, err
	}
	return telegram.Download{Reader: f, Size: info.Size(), FileName: filepath.Base(actual), ContentType: "application/octet-stream"}, nil
}
