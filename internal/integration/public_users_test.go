package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/chats"
	"github.com/JavoxirJava/telegram-gateway/internal/contacts"
	"github.com/JavoxirJava/telegram-gateway/internal/gateway"
	"github.com/JavoxirJava/telegram-gateway/internal/health"
	"github.com/JavoxirJava/telegram-gateway/internal/httpserver"
	"github.com/JavoxirJava/telegram-gateway/internal/media"
	"github.com/JavoxirJava/telegram-gateway/internal/members"
	"github.com/JavoxirJava/telegram-gateway/internal/messages"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/JavoxirJava/telegram-gateway/internal/webauth"
)

// Only the native Telegram transport is replaced. Identity ownership, database
// constraints, browser login, OAuth, REST and MCP use their production code.
type testLogin struct {
	mu            sync.Mutex
	profile       telegram.Profile
	ready, closed bool
}

func (s *testLogin) State() map[string]any {
	return map[string]any{"state": "authorizationStateWaitPhoneNumber"}
}
func (s *testLogin) IsReady() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.ready }
func (s *testLogin) Profile(context.Context) (telegram.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready {
		return telegram.Profile{}, errors.New("not verified")
	}
	return s.profile, nil
}
func (s *testLogin) Authorize(_ context.Context, action, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if action == "phone" {
		return nil
	}
	if action != "code" || value != "verified-test-code" {
		return errors.New("invalid code")
	}
	s.ready = true
	return nil
}
func (s *testLogin) Close() { s.mu.Lock(); defer s.mu.Unlock(); s.closed = true }

type testLoginBackend struct {
	*gateway.Manager
	mu       sync.Mutex
	profiles []telegram.Profile
	sessions map[string]*testLogin
}

func (b *testLoginBackend) NewLoginSession(_ context.Context, id string) (telegram.LoginSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.profiles) == 0 {
		return nil, errors.New("no test identity")
	}
	s := &testLogin{profile: b.profiles[0]}
	b.profiles = b.profiles[1:]
	b.sessions[id] = s
	return s, nil
}
func (b *testLoginBackend) AttachLogin(ctx context.Context, id string, p telegram.Profile) (string, string, error) {
	b.mu.Lock()
	s := b.sessions[id]
	b.mu.Unlock()
	if s == nil {
		return "", "", errors.New("unknown native session")
	}
	s.mu.Lock()
	valid := s.closed && s.ready && s.profile.TelegramUserID == p.TelegramUserID
	s.mu.Unlock()
	if !valid {
		return "", "", errors.New("native verification or close was skipped")
	}
	return b.Manager.AttachLogin(ctx, id, p)
}

type testBrowser struct {
	t    *testing.T
	base string
	c    *http.Client
}

func browser(t *testing.T, base string) *testBrowser {
	jar, _ := cookiejar.New(nil)
	return &testBrowser{t, base, &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (b *testBrowser) request(method, path string, body any, headers map[string]string, expected int) ([]byte, http.Header) {
	b.t.Helper()
	var reader io.Reader
	if body != nil {
		if form, ok := body.(url.Values); ok {
			reader = strings.NewReader(form.Encode())
		} else {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
	}
	r, _ := http.NewRequest(method, b.base+path, reader)
	if body != nil {
		if _, ok := body.(url.Values); ok {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			r.Header.Set("Content-Type", "application/json")
		}
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	res, err := b.c.Do(r)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != expected {
		b.t.Fatalf("%s %s status=%d expected=%d body=%s", method, path, res.StatusCode, expected, raw)
	}
	return raw, res.Header
}
func hidden(t *testing.T, html []byte, attribute, name string) string {
	t.Helper()
	m := regexp.MustCompile(attribute + `="` + name + `"[^>]*value="([^"]+)"`).FindSubmatch(html)
	if len(m) != 2 {
		t.Fatalf("missing field %s", name)
	}
	return string(m[1])
}
func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}
func publicLogin(t *testing.T, b *testBrowser) map[string]any {
	html, _ := b.request("GET", "/login?return_to=https%3A%2F%2Fevil.example", nil, nil, 200)
	h := map[string]string{"X-CSRF-Token": hidden(t, html, "id", "csrf")}
	b.request("POST", "/auth/login/start", map[string]string{"return_to": "https://evil.example"}, nil, 403)
	b.request("POST", "/auth/login/start", map[string]string{"return_to": "https://evil.example"}, h, 200)
	b.request("POST", "/auth/login/action", map[string]string{"action": "phone", "value": "+10000000000"}, h, 200)
	b.request("POST", "/auth/login/action", map[string]string{"action": "code", "value": "wrong"}, h, 400)
	b.request("GET", "/account/data", nil, nil, 401)
	b.request("POST", "/auth/login/action", map[string]string{"action": "code", "value": "verified-test-code"}, h, 200)
	raw, headers := b.request("GET", "/auth/login/state", nil, h, 200)
	d := decode(t, raw)
	if d["state"] != "complete" || d["redirect"] != "/account" {
		t.Fatal("login did not complete safely")
	}
	if !strings.Contains(headers.Get("Set-Cookie"), "HttpOnly") {
		t.Fatal("session cookie is exposed to JavaScript")
	}
	raw, _ = b.request("GET", "/account/data", nil, nil, 200)
	return decode(t, raw)
}

func TestPublicUsersAreIsolated(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := time.Now().UnixNano() % 9000000000000
	aProfile := telegram.Profile{TelegramUserID: id, DisplayName: "Public user A"}
	bProfile := telegram.Profile{TelegramUserID: id + 1, DisplayName: "Public user B"}
	backend := &testLoginBackend{Manager: f.deps.Manager, profiles: []telegram.Profile{aProfile, bProfile, aProfile}, sessions: map[string]*testLogin{}}
	server := httptest.NewUnstartedServer(nil)
	deps := f.deps
	deps.BaseURL = "http://" + server.Listener.Addr().String()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps.WebAuth = webauth.New(f.pool, backend, deps.BaseURL, deps.Limiter, logger)
	handler := httpserver.New(logger, health.NewProbes(map[string]func(context.Context) error{"postgres": f.pool.Ping}), deps)
	server.Config.Handler = handler
	server.Start()
	defer handler.Close()
	defer server.Close()
	a, b := browser(t, server.URL), browser(t, server.URL)
	da, db := publicLogin(t, a), publicLogin(t, b)
	aid, bid := da["account_id"].(string), db["account_id"].(string)
	if aid == bid || da["name"] != "Public user A" || db["name"] != "Public user B" {
		t.Fatal("verified identities were merged")
	}
	var ua, ub string
	if f.pool.QueryRow(ctx, `SELECT user_id::text FROM telegram_accounts WHERE id=$1::uuid`, aid).Scan(&ua) != nil || f.pool.QueryRow(ctx, `SELECT user_id::text FROM telegram_accounts WHERE id=$1::uuid`, bid).Scan(&ub) != nil || ua == ub {
		t.Fatal("users share an owner")
	}
	ha, hb := map[string]string{"X-CSRF-Token": da["csrf"].(string)}, map[string]string{"X-CSRF-Token": db["csrf"].(string)}
	a.request("GET", "/admin/accounts", nil, nil, 401)
	a.request("POST", "/account/token", map[string]any{"account_id": bid}, ha, 400)
	a.request("POST", "/account/token", map[string]any{}, nil, 403)
	raw, _ := a.request("POST", "/account/token", map[string]string{"name": "A client"}, ha, 201)
	ta := decode(t, raw)
	raw, _ = b.request("POST", "/account/token", map[string]string{"name": "B client"}, hb, 201)
	tb := decode(t, raw)
	tokenA, tokenB := ta["token"].(string), tb["token"].(string)
	bearerA, bearerB := map[string]string{"Authorization": "Bearer " + tokenA}, map[string]string{"Authorization": "Bearer " + tokenB}
	chatA, err := f.deps.Chats.Upsert(ctx, chats.Chat{AccountID: aid, TelegramChatID: 42, ChatType: "private", Title: ptr("private marker A")})
	if err != nil {
		t.Fatal(err)
	}
	chatB, err := f.deps.Chats.Upsert(ctx, chats.Chat{AccountID: bid, TelegramChatID: 42, ChatType: "private", Title: ptr("private marker B")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.deps.Messages.Upsert(ctx, messages.Message{AccountID: aid, ChatID: chatA, TelegramMessageID: 1, MessageType: "Text", Content: ptr("private marker A"), SentAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	messageB, err := f.deps.Messages.Upsert(ctx, messages.Message{AccountID: bid, ChatID: chatB, TelegramMessageID: 1, MessageType: "Text", Content: ptr("private marker B"), SentAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.deps.Contacts.Upsert(ctx, bid, contacts.Contact{TelegramUserID: 123, FirstName: ptr("private marker B")}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.deps.Members.Upsert(ctx, bid, chatB, members.Member{TelegramPeerID: 123, FirstName: ptr("private marker B")}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/chats", "/v1/chats/search?q=private", "/v1/messages/search?q=private", "/v1/contacts", "/v1/contacts/search?q=private", "/v1/chats/" + chatB + "/messages", "/v1/chats/" + chatB + "/members"} {
		raw, _ = a.request("GET", path, nil, bearerA, 200)
		if bytes.Contains(raw, []byte("private marker B")) || bytes.Contains(raw, []byte(chatB)) {
			t.Fatal("cross-user REST data leak", path)
		}
	}
	raw, _ = b.request("GET", "/v1/chats/"+chatB+"/messages", nil, bearerB, 200)
	if !bytes.Contains(raw, []byte("private marker B")) {
		t.Fatal("own messages missing")
	}
	raw, _ = a.request("POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "get_messages", "arguments": map[string]any{"chat_id": chatB}}}, map[string]string{"Authorization": "Bearer " + tokenA, "Accept": "application/json, text/event-stream"}, 200)
	if bytes.Contains(raw, []byte("private marker B")) || bytes.Contains(raw, []byte(messageB)) {
		t.Fatal("cross-user MCP data leak")
	}
	repo := media.NewRepository(f.pool)
	fileID := int64(12)
	unique := "test-public-media"
	mediaB, err := repo.RegisterPending(ctx, messageB, "document", &fileID, &unique, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.ClaimDownloadRecoverable(ctx, mediaB, 5); err != nil || !ok {
		t.Fatal(err)
	}
	if err := f.deps.Media.StoreDownload(ctx, bid, mediaB, strings.NewReader("B file"), 6, "text/plain"); err != nil {
		t.Fatal(err)
	}
	a.request("GET", "/v1/media/"+mediaB+"/url", nil, bearerA, 404)
	b.request("GET", "/v1/media/"+mediaB+"/url", nil, bearerB, 200)
	raw, _ = a.request("GET", "/v1/chats/"+chatB+"/media", nil, bearerA, 200)
	if bytes.Contains(raw, []byte(mediaB)) {
		t.Fatal("cross-user media metadata leak")
	}
	a.request("POST", "/account/clients/"+tb["client_id"].(string)+"/revoke", map[string]any{}, ha, 404)
	b.request("GET", "/v1/profile", nil, bearerB, 200)
	testPublicOAuthBinding(t, f, a, b, aid, bid, ha)
	a.request("POST", "/account/clients/"+ta["client_id"].(string)+"/revoke", map[string]any{}, ha, 200)
	a.request("GET", "/v1/profile", nil, bearerA, 401)
	b.request("GET", "/v1/profile", nil, bearerB, 200)
	a.request("POST", "/account/logout", map[string]any{}, ha, 200)
	a.request("GET", "/account/data", nil, nil, 401)
	again := publicLogin(t, a)
	if again["account_id"] != aid {
		t.Fatal("returning identity received another account")
	}
	var n int
	if f.pool.QueryRow(ctx, `SELECT count(*) FROM telegram_accounts WHERE telegram_user_id=$1`, id).Scan(&n) != nil || n != 1 {
		t.Fatal("returning user duplicated their account")
	}
}

func testPublicOAuthBinding(t *testing.T, f *fixture, a, b *testBrowser, aid, bid string, ha map[string]string) {
	raw, _ := a.request("POST", "/oauth/register", map[string]any{"client_name": "Public OAuth", "redirect_uris": []string{"http://127.0.0.1:44444/callback"}, "token_endpoint_auth_method": "none"}, nil, 201)
	cid := decode(t, raw)["client_id"].(string)
	verifier := strings.Repeat("v", 64)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {cid}, "redirect_uri": {"http://127.0.0.1:44444/callback"}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "scope": {"profile:read"}, "state": {"public-state"}}
	anon := browser(t, a.base)
	raw, h := anon.request("GET", "/oauth/authorize?"+q.Encode(), nil, nil, 303)
	if !strings.HasPrefix(h.Get("Location"), "/login?") || bytes.Contains(raw, []byte(aid)) || bytes.Contains(raw, []byte("Public user")) {
		t.Fatal("anonymous consent exposes an account")
	}
	raw, h = a.request("GET", "/oauth/authorize?"+q.Encode(), nil, nil, 200)
	if !bytes.Contains(raw, []byte("Public user A")) || bytes.Contains(raw, []byte("Public user B")) || bytes.Contains(raw, []byte(`name="password"`)) {
		t.Fatal("consent must show only the signed-in account")
	}
	form := url.Values{"request_id": {hidden(t, raw, "name", "request_id")}, "csrf": {hidden(t, raw, "name", "csrf")}, "decision": {"allow"}, "account_id": {bid}}
	a.request("POST", "/oauth/authorize", form, nil, 403)
	cookie := (&http.Response{Header: h}).Cookies()[0]
	b.request("POST", "/oauth/authorize", form, map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}, 400)
	form.Set("account_id", aid)
	_, h = a.request("POST", "/oauth/authorize", form, nil, 303)
	redirect, _ := url.Parse(h.Get("Location"))
	if redirect.Query().Get("iss") != a.base {
		t.Fatal("issuer missing")
	}
	raw, _ = a.request("POST", "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {cid}, "code": {redirect.Query().Get("code")}, "redirect_uri": {q.Get("redirect_uri")}, "code_verifier": {verifier}}, nil, 200)
	tokens := decode(t, raw)
	token := tokens["access_token"].(string)
	raw, _ = a.request("GET", "/v1/profile", nil, map[string]string{"Authorization": "Bearer " + token}, 200)
	if !bytes.Contains(raw, []byte(aid)) || bytes.Contains(raw, []byte(bid)) {
		t.Fatal("OAuth granted the wrong account")
	}
	var grant string
	digest := sha256.Sum256([]byte(token))
	if err := f.pool.QueryRow(context.Background(), `UPDATE access_tokens SET expires_at=NOW()-interval '1 minute' WHERE token_hash=$1 RETURNING client_id::text`, digest[:]).Scan(&grant); err != nil {
		t.Fatal(err)
	}
	raw, _ = a.request("GET", "/account/data", nil, nil, 200)
	active := false
	for _, item := range decode(t, raw)["clients"].([]any) {
		client := item.(map[string]any)
		if client["id"] == grant {
			active = client["active"] == true
		}
	}
	if !active {
		t.Fatal("a live refresh grant must remain visible and revocable after access expiry")
	}
	// Rotation and revocation may arrive together. Every token returned by the
	// concurrent rotation must be unusable once revocation has completed.
	refreshForm := url.Values{"grant_type": {"refresh_token"}, "client_id": {cid}, "refresh_token": {tokens["refresh_token"].(string)}}
	rr, _ := http.NewRequest("POST", a.base+"/oauth/token", strings.NewReader(refreshForm.Encode()))
	rr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rv, _ := http.NewRequest("POST", a.base+"/account/clients/"+grant+"/revoke", strings.NewReader("{}"))
	rv.Header.Set("Content-Type", "application/json")
	rv.Header.Set("X-CSRF-Token", ha["X-CSRF-Token"])
	type outcome struct {
		status int
		body   []byte
		err    error
	}
	start := make(chan struct{})
	rotate, revoke := make(chan outcome, 1), make(chan outcome, 1)
	send := func(req *http.Request, out chan<- outcome) {
		<-start
		res, err := a.c.Do(req)
		if err != nil {
			out <- outcome{err: err}
			return
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		out <- outcome{res.StatusCode, body, err}
	}
	go send(rr, rotate)
	go send(rv, revoke)
	close(start)
	rotated, revoked := <-rotate, <-revoke
	if rotated.err != nil || revoked.err != nil || revoked.status != 200 || (rotated.status != 200 && rotated.status != 400) {
		t.Fatal("concurrent rotation/revocation failed", rotated.status, revoked.status, rotated.err, revoked.err)
	}
	if rotated.status == 200 {
		newTokens := decode(t, rotated.body)
		a.request("GET", "/v1/profile", nil, map[string]string{"Authorization": "Bearer " + newTokens["access_token"].(string)}, 401)
		refreshForm.Set("refresh_token", newTokens["refresh_token"].(string))
	}
	a.request("POST", "/oauth/token", refreshForm, nil, 400)
}
