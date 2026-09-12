// Package tdlib provides the authenticated TDLib lifecycle, deliberately kept
// separate from the public read-only API and future MCP transport.
package tdlib

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/tdjson"
)

const (
	WaitParameters = "authorizationStateWaitTdlibParameters"
	WaitPhone      = "authorizationStateWaitPhoneNumber"
	WaitCode       = "authorizationStateWaitCode"
	WaitPassword   = "authorizationStateWaitPassword"
	WaitEmail      = "authorizationStateWaitEmailAddress"
	WaitEmailCode  = "authorizationStateWaitEmailCode"
	WaitDevice     = "authorizationStateWaitOtherDeviceConfirmation"
	Ready          = "authorizationStateReady"
	Closed         = "authorizationStateClosed"
)

var ErrNotReady = errors.New("Telegram session is not ready")
var ErrAuthState = errors.New("authentication step is not valid in the current state")

type Config struct {
	APIID             int
	APIHash           string
	DatabaseDirectory string
	FilesDirectory    string
	DatabaseKey       []byte
	UseTestDC         bool
	Requests          RequestPolicy
}

// Authorization contains credentials only in QRLink, when requested explicitly
// by the local authenticated control plane. Never log this object.
type Authorization struct {
	Type   string `json:"type"`
	QRLink string `json:"qr_link,omitempty"`
}

type Session struct {
	client             *tdjson.Client
	config             Config
	sink               tdjson.UpdateHandler
	mu                 sync.Mutex
	auth               Authorization
	changed            chan struct{}
	authGate           chan struct{}
	closed             bool
	authorizationEnded bool
	requestGate        chan struct{}
}

func New(engine *tdjson.Engine, cfg Config, sink tdjson.UpdateHandler) (*Session, error) {
	if engine == nil || cfg.Requests == nil || cfg.APIID <= 0 || cfg.APIID > 2147483647 || len(cfg.APIHash) != 32 || len(cfg.DatabaseKey) != 32 || cfg.DatabaseDirectory == "" || cfg.FilesDirectory == "" {
		return nil, errors.New("TDLib configuration is incomplete or invalid")
	}
	s := &Session{config: cfg, sink: sink, changed: make(chan struct{}), authGate: make(chan struct{}, 1), requestGate: make(chan struct{}, 1)}
	client, err := engine.NewClient(s.onUpdate, cfg.Requests.After)
	if err != nil {
		return nil, err
	}
	s.client = client
	return s, nil
}

// Initialize starts the native instance and supplies its encrypted database
// parameters. It does not submit credentials or automatically retry login.
func (s *Session) Initialize(ctx context.Context) error {
	raw, err := s.client.Call(ctx, "getAuthorizationState", nil)
	if err != nil {
		return err
	}
	if err := s.setState(raw, true); err != nil {
		return err
	}
	state, err := s.waitState(ctx, func(a Authorization) bool { return a.Type != "" })
	if err != nil {
		return err
	}
	if state.Type != WaitParameters {
		return nil
	}
	fields := map[string]any{
		"use_test_dc":             s.config.UseTestDC,
		"database_directory":      s.config.DatabaseDirectory,
		"files_directory":         s.config.FilesDirectory,
		"database_encryption_key": base64.StdEncoding.EncodeToString(s.config.DatabaseKey),
		"use_file_database":       true, "use_chat_info_database": true, "use_message_database": true,
		"use_secret_chats": false,
		"api_id":           s.config.APIID, "api_hash": s.config.APIHash,
		"system_language_code": "en", "device_model": "Gateway (user-authorized)",
		"system_version": "Linux", "application_version": "0.2.0",
	}
	if _, err := s.client.Call(ctx, "setTdlibParameters", fields); err != nil {
		return err
	}
	_, err = s.waitState(ctx, func(a Authorization) bool { return a.Type != WaitParameters && a.Type != "" })
	return err
}

func (s *Session) onUpdate(ctx context.Context, raw json.RawMessage) error {
	var u struct {
		Type  string          `json:"@type"`
		State json.RawMessage `json:"authorization_state"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return errors.New("invalid TDLib update")
	}
	if u.Type == "updateAuthorizationState" {
		return s.acceptState(u.State)
	}
	s.mu.Lock()
	ended := s.authorizationEnded
	s.mu.Unlock()
	if ended {
		return errors.New("verified native authorization ended; restart required")
	}
	if s.sink != nil {
		return s.sink(ctx, raw)
	}
	return nil
}
func (s *Session) acceptState(raw json.RawMessage) error { return s.setState(raw, false) }

func (s *Session) setState(raw json.RawMessage, onlyIfEmpty bool) error {
	var a struct {
		Type string `json:"@type"`
		Link string `json:"link"`
	}
	if err := json.Unmarshal(raw, &a); err != nil || !strings.HasPrefix(a.Type, "authorizationState") {
		return errors.New("invalid TDLib authorization state")
	}
	state := Authorization{Type: a.Type}
	if a.Type == WaitDevice {
		if !strings.HasPrefix(a.Link, "tg://login?token=") || len(a.Link) > 4096 {
			return errors.New("invalid TDLib QR login link")
		}
		state.QRLink = a.Link
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || (onlyIfEmpty && s.auth.Type != "") {
		return nil
	}
	if s.auth.Type == Ready && state.Type != Ready {
		s.authorizationEnded = true
	}
	s.auth = state
	close(s.changed)
	s.changed = make(chan struct{})
	if state.Type == Closed {
		s.closed = true
	}
	return nil
}
func (s *Session) State() Authorization { s.mu.Lock(); defer s.mu.Unlock(); return s.auth }
func (s *Session) waitState(ctx context.Context, accept func(Authorization) bool) (Authorization, error) {
	for {
		s.mu.Lock()
		state := s.auth
		changed := s.changed
		s.mu.Unlock()
		if state.Type == Closed {
			return state, tdjson.ErrClosed
		}
		if accept(state) {
			return state, nil
		}
		select {
		case <-ctx.Done():
			return Authorization{}, ctx.Err()
		case <-s.client.Done():
			return Authorization{}, tdjson.ErrClosed
		case <-changed:
		}
	}
}
func (s *Session) WaitReady(ctx context.Context) error {
	_, err := s.waitState(ctx, func(a Authorization) bool { return a.Type == Ready })
	return err
}
func (s *Session) Done() <-chan struct{}           { return s.client.Done() }
func (s *Session) Close(ctx context.Context) error { return s.client.Close(ctx) }

func (s *Session) authenticate(ctx context.Context, state, method string, fields map[string]any) error {
	select {
	case s.authGate <- struct{}{}:
		defer func() { <-s.authGate }()
	default:
		return errors.New("another authentication request is in progress")
	}
	if s.State().Type != state {
		return ErrAuthState
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := s.governedCall(requestCtx, method, fields)
	return err
}
func (s *Session) SubmitPhone(ctx context.Context, phone string) error {
	if len(phone) < 8 || len(phone) > 16 || phone[0] != '+' {
		return errors.New("phone number must use E.164 format")
	}
	for _, c := range phone[1:] {
		if c < '0' || c > '9' {
			return errors.New("phone number must use E.164 format")
		}
	}
	return s.authenticate(ctx, WaitPhone, "setAuthenticationPhoneNumber", map[string]any{"phone_number": phone, "settings": nil})
}
func (s *Session) SubmitCode(ctx context.Context, code string) error {
	if len(code) == 0 || len(code) > 32 {
		return errors.New("invalid login code length")
	}
	return s.authenticate(ctx, WaitCode, "checkAuthenticationCode", map[string]any{"code": code})
}
func (s *Session) SubmitPassword(ctx context.Context, password string) error {
	if len(password) == 0 || len(password) > 4096 {
		return errors.New("invalid password length")
	}
	return s.authenticate(ctx, WaitPassword, "checkAuthenticationPassword", map[string]any{"password": password})
}
func (s *Session) SubmitEmail(ctx context.Context, email string) error {
	if len(email) > 320 || !strings.Contains(email, "@") {
		return errors.New("invalid email")
	}
	return s.authenticate(ctx, WaitEmail, "setAuthenticationEmailAddress", map[string]any{"email_address": email})
}
func (s *Session) SubmitEmailCode(ctx context.Context, code string) error {
	if len(code) == 0 || len(code) > 32 {
		return errors.New("invalid email code length")
	}
	return s.authenticate(ctx, WaitEmailCode, "checkAuthenticationEmailCode", map[string]any{"code": map[string]any{"@type": "emailAddressAuthenticationCode", "code": code}})
}
func (s *Session) RequestQR(ctx context.Context) error {
	return s.authenticate(ctx, WaitPhone, "requestQrCodeAuthentication", map[string]any{"other_user_ids": []int64{}})
}

// Read is an internal transport seam, not exposed to MCP/HTTP clients. It never
// accepts authentication or Telegram write operations. Typed adapters can be
// built on this without giving them access to login/session credentials.
func (s *Session) Read(ctx context.Context, method string, fields map[string]any) (json.RawMessage, error) {
	switch method {
	case "getMe", "getMessage", "getMessages", "getChat", "getUser", "loadChats", "getChatHistory", "getContacts", "getSupergroupMembers", "getBasicGroupFullInfo", "downloadFile", "getFile":
	default:
		return nil, tdjson.ErrReadOnly
	}
	s.mu.Lock()
	valid := s.auth.Type == Ready && !s.authorizationEnded
	s.mu.Unlock()
	if !valid {
		return nil, ErrNotReady
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	raw, err := s.governedCall(ctx, method, fields)
	if err != nil {
		return nil, fmt.Errorf("Telegram read: %w", err)
	}
	s.mu.Lock()
	valid = s.auth.Type == Ready && !s.authorizationEnded
	s.mu.Unlock()
	if !valid {
		return nil, ErrNotReady
	}
	return raw, nil
}
