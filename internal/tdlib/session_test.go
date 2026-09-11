package tdlib

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/tdjson"
	"github.com/JavoxirJava/telegram-gateway/internal/tdjson/tdjsontest"
)

func newTestSession(t *testing.T) (*Session, *tdjsontest.Transport) {
	t.Helper()
	tr := tdjsontest.New()
	tr.OnSend = func(id int, r map[string]any) {
		switch r["@type"] {
		case "getAuthorizationState":
			tr.State(id, WaitParameters)
			tr.Reply(id, r, map[string]any{"@type": WaitParameters})
		case "setTdlibParameters":
			key, _ := base64.StdEncoding.DecodeString(r["database_encryption_key"].(string))
			if !bytes.Equal(key, bytes.Repeat([]byte{2}, 32)) {
				t.Error("database key encoded incorrectly")
			}
			if r["use_secret_chats"] != false {
				t.Error("secret chats enabled")
			}
			tr.State(id, WaitPhone)
			tr.Reply(id, r, map[string]any{"@type": "ok"})
		case "setAuthenticationPhoneNumber":
			tr.State(id, WaitCode)
			tr.Reply(id, r, map[string]any{"@type": "ok"})
		case "checkAuthenticationCode":
			if r["code"] == "bad" {
				tr.Reply(id, r, map[string]any{"@type": "error", "code": 400, "message": "invalid PRIVATE_CODE"})
				return
			}
			tr.State(id, WaitPassword)
			tr.Reply(id, r, map[string]any{"@type": "ok"})
		case "checkAuthenticationPassword":
			tr.State(id, Ready)
			tr.Reply(id, r, map[string]any{"@type": "ok"})
		case "requestQrCodeAuthentication":
			tr.Emit(id, map[string]any{"@type": "updateAuthorizationState", "authorization_state": map[string]any{"@type": WaitDevice, "link": "tg://login?token=test-only"}})
			tr.Reply(id, r, map[string]any{"@type": "ok"})
		default:
			tr.DefaultSend(id, r)
		}
	}
	e, err := tdjson.New(tr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = e.Close(ctx)
	})
	s, err := New(e, Config{APIID: 123, APIHash: "0123456789abcdef0123456789abcdef", DatabaseDirectory: "/tmp/test/db", FilesDirectory: "/tmp/test/files", DatabaseKey: bytes.Repeat([]byte{2}, 32)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	return s, tr
}
func TestAuthenticationLifecycle(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	if s.State().Type != WaitPhone {
		t.Fatal(s.State().Type)
	}
	if _, err := s.Read(ctx, "getMe", nil); !errors.Is(err, ErrNotReady) {
		t.Fatal(err)
	}
	if err := s.SubmitPassword(ctx, "private"); !errors.Is(err, ErrAuthState) {
		t.Fatal(err)
	}
	if err := s.SubmitPhone(ctx, "+998901234567"); err != nil {
		t.Fatal(err)
	}
	if err := s.SubmitCode(ctx, "bad"); err == nil {
		t.Fatal("invalid code accepted")
	}
	if s.State().Type != WaitCode {
		t.Fatal("failed auth advanced state")
	}
	if err := s.SubmitCode(ctx, "12345"); err != nil {
		t.Fatal(err)
	}
	if err := s.SubmitPassword(ctx, "private"); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, "sendMessage", nil); !errors.Is(err, tdjson.ErrReadOnly) {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, "checkAuthenticationCode", nil); !errors.Is(err, tdjson.ErrReadOnly) {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, "getMe", nil); err != nil {
		t.Fatal(err)
	}
}
func TestQRAndMalformedAuth(t *testing.T) {
	s, _ := newTestSession(t)
	if err := s.RequestQR(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state := s.State(); state.Type != WaitDevice || state.QRLink == "" {
		t.Fatal("QR state not received")
	}
	if err := s.acceptState(json.RawMessage(`{"@type":"authorizationStateWaitOtherDeviceConfirmation","link":"https://attacker.invalid"}`)); err == nil {
		t.Fatal("invalid QR scheme accepted")
	}
}
func TestCredentialsValidation(t *testing.T) {
	s, _ := newTestSession(t)
	for _, phone := range []string{"", "998901234567", "+99890\n1234", "+123"} {
		if err := s.SubmitPhone(context.Background(), phone); err == nil {
			t.Fatal("invalid phone accepted")
		}
	}
}
