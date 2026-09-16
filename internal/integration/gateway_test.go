package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"

	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/accounts"
	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	"github.com/JavoxirJava/telegram-gateway/internal/chats"
	"github.com/JavoxirJava/telegram-gateway/internal/config"
	"github.com/JavoxirJava/telegram-gateway/internal/contacts"
	"github.com/JavoxirJava/telegram-gateway/internal/gateway"
	"github.com/JavoxirJava/telegram-gateway/internal/health"
	"github.com/JavoxirJava/telegram-gateway/internal/httpserver"
	"github.com/JavoxirJava/telegram-gateway/internal/media"
	"github.com/JavoxirJava/telegram-gateway/internal/members"
	"github.com/JavoxirJava/telegram-gateway/internal/messages"
	"github.com/JavoxirJava/telegram-gateway/internal/natsbus"
	"github.com/JavoxirJava/telegram-gateway/internal/objectstore"
	"github.com/JavoxirJava/telegram-gateway/internal/ratelimit"
	"github.com/JavoxirJava/telegram-gateway/internal/redisstore"
	"github.com/JavoxirJava/telegram-gateway/internal/syncjob"
	"github.com/JavoxirJava/telegram-gateway/migrations"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fixture struct {
	pool                  *pgxpool.Pool
	server                *httptest.Server
	account, token, admin string
	deps                  httpserver.Dependencies
	store                 *objectstore.Store
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	u, _ := url.Parse(dsn)
	if u == nil || !strings.HasSuffix(u.Path, "_test") {
		t.Fatal("integration tests require a dedicated _test database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(ctx, pool); err != nil {
		t.Fatalf("migration replay: %v", err)
	}
	redis, err := redisstore.Open(ctx, config.RedisConfig{Addr: "127.0.0.1:16386", DB: 15})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { redis.Close() })
	if err := redis.FlushDB(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	bus, err := natsbus.Open(config.NATSConfig{URL: "nats://127.0.0.1:14286"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bus.Close)
	pub := syncjob.NewPublisher(bus.JetStream)
	if err := pub.EnsureStream(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := objectstore.Open(ctx, config.MinIOConfig{Endpoint: "127.0.0.1:19086", AccessKey: os.Getenv("TEST_MINIO_USER"), SecretKey: os.Getenv("TEST_MINIO_PASSWORD"), Bucket: "gateway-tests"})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := gateway.New(pool, config.TelegramConfig{}, make([]byte, 32), pub, logger)
	if err := manager.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := manager.CreateAccount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a, err = accounts.NewRepository(pool).Activate(ctx, a.ID, time.Now().UnixNano()%9000000000000, "Integration Account", "")
	if err != nil {
		t.Fatal(err)
	}
	client, err := access.NewRepository(pool).CreateClient(ctx, manager.OwnerID(), "integration", "MCP")
	if err != nil {
		t.Fatal(err)
	}
	scopes := []access.Scope{access.ScopeProfileRead, access.ScopeChatsList, access.ScopeChatRead, access.ScopeMessagesRead, access.ScopeMessagesSearch, access.ScopeContactsRead, access.ScopeMediaRead, access.ScopeMembersRead}
	token, err := access.NewRepository(pool).IssueToken(ctx, client, a.ID, scopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{pool: pool, account: a.ID, token: token.Plaintext, admin: strings.Repeat("s", 48), store: store}
	f.deps = httpserver.Dependencies{Pool: pool, Manager: manager, AdminToken: f.admin, Access: access.NewRepository(pool), Accounts: accounts.NewRepository(pool), Audit: audit.NewWriter(pool), Chats: chats.NewRepository(pool), Contacts: contacts.NewRepository(pool), Members: members.NewRepository(pool), Messages: messages.NewRepository(pool), Media: media.NewService(media.NewRepository(pool), store), Limiter: ratelimit.New(redis)}
	server := httptest.NewUnstartedServer(nil)
	f.deps.BaseURL = "http://" + server.Listener.Addr().String()
	handler := httpserver.New(logger, health.NewProbes(map[string]func(context.Context) error{"postgres": pool.Ping, "minio": store.Check}), f.deps)
	server.Config.Handler = handler
	t.Cleanup(handler.Close)
	server.Start()
	t.Cleanup(server.Close)
	f.server = server
	return f
}
func (f *fixture) request(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, f.server.URL+path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}
func TestGatewayIntegration(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	t.Run("health and unauthorized", func(t *testing.T) {
		status, _ := f.request(t, "GET", "/health/ready", "", nil)
		if status != 200 {
			t.Fatal(status)
		}
		status, _ = f.request(t, "GET", "/v1/profile", "", nil)
		if status != 401 {
			t.Fatal(status)
		}
		status, _ = f.request(t, "POST", "/mcp", "", map[string]any{})
		if status != 401 {
			t.Fatal(status)
		}
	})
	chatID, err := f.deps.Chats.Upsert(ctx, chats.Chat{AccountID: f.account, TelegramChatID: 1234, ChatType: "private", Title: ptr("Integration chat")})
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := f.deps.Messages.Upsert(ctx, messages.Message{AccountID: f.account, ChatID: chatID, TelegramMessageID: 100, MessageType: "Text", Content: ptr("hello integration"), SentAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("REST and MCP", func(t *testing.T) {
		status, body := f.request(t, "GET", "/v1/chats", f.token, nil)
		if status != 200 || !bytes.Contains(body, []byte(chatID)) {
			t.Fatalf("chats status=%d body=%s", status, body)
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "integration", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: f.server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token: f.token}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		tools, err := session.ListTools(ctx, nil)
		if err != nil || len(tools.Tools) != 10 {
			t.Fatalf("tools=%v err=%v", tools, err)
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_messages", Arguments: map[string]any{"chat_id": chatID}})
		if err != nil || result.IsError {
			t.Fatalf("MCP read failed: %v %v", result, err)
		}
		b, _ := json.Marshal(result)
		if !bytes.Contains(b, []byte("hello integration")) {
			t.Fatal(string(b))
		}
		result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "get_messages", Arguments: map[string]any{"chat_id": "../../manage"}})
		if err == nil && !result.IsError {
			t.Fatal("invalid chat accepted")
		}
	})
	t.Run("scope and tenant isolation", func(t *testing.T) {
		c, err := f.deps.Access.CreateClient(ctx, f.deps.Manager.OwnerID(), "profile only", "MCP")
		if err != nil {
			t.Fatal(err)
		}
		tok, err := f.deps.Access.IssueToken(ctx, c, f.account, []access.Scope{access.ScopeProfileRead}, nil)
		if err != nil {
			t.Fatal(err)
		}
		status, _ := f.request(t, "GET", "/v1/chats", tok.Plaintext, nil)
		if status != 403 {
			t.Fatal(status)
		}
		other, err := f.deps.Manager.CreateAccount(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.deps.Accounts.Activate(ctx, other.ID, time.Now().UnixNano()%9000000000000, "Other", "")
		if err != nil {
			t.Fatal(err)
		}
		otherChat, err := f.deps.Chats.Upsert(ctx, chats.Chat{AccountID: other.ID, TelegramChatID: 1234, ChatType: "private"})
		if err != nil {
			t.Fatal(err)
		}
		status, body := f.request(t, "GET", "/v1/chats/"+otherChat+"/messages", f.token, nil)
		if status != 200 || !bytes.Contains(body, []byte(`"count":0`)) {
			t.Fatalf("tenant read: %d %s", status, body)
		}
	})
	t.Run("media ticket and revocation", func(t *testing.T) {
		fileID := int64(123)
		size := int64(13)
		mediaID, err := media.NewRepository(f.pool).RegisterPending(ctx, messageID, "document", &fileID, nil, ptr("text/plain"), ptr("test.txt"), &size)
		if err != nil {
			t.Fatal(err)
		}
		if claimed, err := media.NewRepository(f.pool).ClaimDownloadRecoverable(ctx, mediaID, 5); err != nil || !claimed {
			t.Fatalf("claim media: %v", err)
		}
		if err := f.deps.Media.StoreDownload(ctx, f.account, mediaID, strings.NewReader("media fixture"), size, "text/plain"); err != nil {
			t.Fatal(err)
		}
		status, body := f.request(t, "GET", "/v1/media/"+mediaID+"/url", f.token, nil)
		if status != 200 {
			t.Fatalf("%d %s", status, body)
		}
		var out struct {
			Data struct {
				URL string `json:"url"`
			} `json:"data"`
		}
		json.Unmarshal(body, &out)
		res, err := http.Get(out.Data.URL)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || string(b) != "media fixture" {
			t.Fatalf("download %d %s", res.StatusCode, b)
		}
		if _, err := f.pool.Exec(ctx, `INSERT INTO message_tombstones(account_id,telegram_chat_id,telegram_message_id) VALUES($1::uuid,1234,100)`, f.account); err != nil {
			t.Fatal(err)
		}
		res, err = http.Get(out.Data.URL)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Fatal("deleted media remains downloadable")
		}
	})
	t.Run("late backfill respects deletion", func(t *testing.T) {
		status, body := f.request(t, "GET", "/v1/chats/"+chatID+"/messages", f.token, nil)
		if status != 200 || bytes.Contains(body, []byte("hello integration")) {
			t.Fatalf("deleted message leaked: %d %s", status, body)
		}
	})
	t.Run("OAuth PKCE rotation and CSRF", func(t *testing.T) { testOAuth(t, f) })
}

type bearerTransport struct{ token string }

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(r)
}
func ptr(v string) *string { return &v }
func testOAuth(t *testing.T, f *fixture) {
	status, body := f.request(t, "POST", "/oauth/register", "", map[string]any{"client_name": "OAuth test", "redirect_uris": []string{"http://127.0.0.1:44444/callback"}, "token_endpoint_auth_method": "none"})
	if status != 201 {
		t.Fatalf("register %d %s", status, body)
	}
	var c map[string]any
	json.Unmarshal(body, &c)
	clientID := c["client_id"].(string)
	verifier := strings.Repeat("a", 64)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{"client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:44444/callback"}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {challenge}, "scope": {"profile:read"}, "state": {"test-state"}, "resource": {f.server.URL + "/mcp"}}
	sessionRequest, _ := http.NewRequest("POST", f.server.URL+"/admin/accounts/"+f.account+"/browser-session", nil)
	sessionRequest.Header.Set("Authorization", "Bearer "+f.admin)
	sessionResponse, err := http.DefaultClient.Do(sessionRequest)
	if err != nil {
		t.Fatal(err)
	}
	sessionResponse.Body.Close()
	if sessionResponse.StatusCode != 200 {
		t.Fatal("browser session failed")
	}
	browserCookies := sessionResponse.Cookies()
	authRequest, _ := http.NewRequest("GET", f.server.URL+"/oauth/authorize?"+q.Encode(), nil)
	for _, cookie := range browserCookies {
		authRequest.AddCookie(cookie)
	}
	res, err := http.DefaultClient.Do(authRequest)
	if err != nil {
		t.Fatal(err)
	}
	html, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("authorize %d %s", res.StatusCode, html)
	}
	extract := func(name string) string {
		marker := `name="` + name + `" value="`
		_, rest, found := strings.Cut(string(html), marker)
		if !found {
			t.Fatalf("field %s missing", name)
		}
		value, _, _ := strings.Cut(rest, `"`)
		return value
	}
	form := url.Values{"request_id": {extract("request_id")}, "csrf": {extract("csrf")}, "decision": {"allow"}, "account_id": {f.account}}
	client := &http.Client{CheckRedirect: func(r *http.Request, v []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(path string, values url.Values, cookies []*http.Cookie) (int, []byte, string) {
		req, _ := http.NewRequest("POST", f.server.URL+path, strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range browserCookies {
			req.AddCookie(c)
		}
		for _, c := range cookies {
			req.AddCookie(c)
		}
		r, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, b, r.Header.Get("Location")
	}
	status, _, _ = post("/oauth/authorize", form, nil)
	if status != 400 {
		t.Fatal("CSRF without cookie accepted")
	}
	status, body, location := post("/oauth/authorize", form, res.Cookies())
	if status != 303 {
		t.Fatalf("consent %d %s", status, body)
	}
	redirect, _ := url.Parse(location)
	if redirect.Query().Get("iss") != f.server.URL || redirect.Query().Get("state") != "test-state" {
		t.Fatal("issuer/state missing")
	}
	tf := url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {redirect.Query().Get("code")}, "redirect_uri": {"http://127.0.0.1:44444/callback"}, "code_verifier": {strings.Repeat("b", 64)}, "resource": {f.server.URL + "/mcp"}}
	status, _, _ = post("/oauth/token", tf, nil)
	if status != 400 {
		t.Fatal("wrong PKCE accepted")
	}
	tf.Set("code_verifier", verifier)
	status, body, _ = post("/oauth/token", tf, nil)
	if status != 200 {
		t.Fatalf("token %d %s", status, body)
	}
	var tokens map[string]any
	json.Unmarshal(body, &tokens)
	status, _, _ = post("/oauth/token", tf, nil)
	if status != 400 {
		t.Fatal("authorization code replay accepted")
	}
	status, _ = f.request(t, "GET", "/v1/profile", tokens["access_token"].(string), nil)
	if status != 200 {
		t.Fatal(status)
	}
	rf := url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {tokens["refresh_token"].(string)}}
	status, body, _ = post("/oauth/token", rf, nil)
	if status != 200 {
		t.Fatalf("refresh %d %s", status, body)
	}
	status, _, _ = post("/oauth/token", rf, nil)
	if status != 400 {
		t.Fatal("refresh replay accepted")
	}
}
