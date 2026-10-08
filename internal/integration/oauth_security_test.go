package integration

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/oauth"
)

type securityGrant struct {
	b                       *testBrowser
	client, access, refresh string
}

// Exercise real consent and PKCE; the operator bridge supplies only the browser
// login for an isolated fixture account. No native Telegram calls are made.
func newSecurityGrant(t *testing.T, f *fixture, client string) securityGrant {
	t.Helper()
	b := browser(t, f.server.URL)
	b.request("POST", "/admin/accounts/"+f.account+"/browser-session", map[string]any{}, map[string]string{"Authorization": "Bearer " + f.admin}, 200)
	if client == "" {
		raw, _ := b.request("POST", "/oauth/register", map[string]any{"client_name": "security regression", "redirect_uris": []string{"https://example.invalid/callback"}}, nil, 201)
		client = decode(t, raw)["client_id"].(string)
	}
	verifier := strings.Repeat("v", 64)
	digest := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {client}, "redirect_uri": {"https://example.invalid/callback"}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "scope": {"profile:read"}}
	raw, _ := b.request("GET", "/oauth/authorize?"+q.Encode(), nil, nil, 200)
	_, h := b.request("POST", "/oauth/authorize", url.Values{"request_id": {hidden(t, raw, "name", "request_id")}, "csrf": {hidden(t, raw, "name", "csrf")}, "decision": {"allow"}, "account_id": {f.account}}, nil, 303)
	callback, _ := url.Parse(h.Get("Location"))
	raw, _ = b.request("POST", "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {client}, "code": {callback.Query().Get("code")}, "redirect_uri": {q.Get("redirect_uri")}, "code_verifier": {verifier}}, nil, 200)
	tokens := decode(t, raw)
	return securityGrant{b, client, tokens["access_token"].(string), tokens["refresh_token"].(string)}
}
func (g securityGrant) form() url.Values {
	return url.Values{"grant_type": {"refresh_token"}, "client_id": {g.client}, "refresh_token": {g.refresh}}
}
func (g securityGrant) rotate(t *testing.T) securityGrant {
	t.Helper()
	raw, _ := g.b.request("POST", "/oauth/token", g.form(), nil, 200)
	tokens := decode(t, raw)
	return securityGrant{g.b, g.client, tokens["access_token"].(string), tokens["refresh_token"].(string)}
}
func (g securityGrant) profile(status int) {
	g.b.request("GET", "/v1/profile", nil, map[string]string{"Authorization": "Bearer " + g.access}, status)
}

func TestOAuthSecurityRefreshReplayClosesOnlyItsGrant(t *testing.T) {
	f := setup(t)
	original := newSecurityGrant(t, f, "")
	other := newSecurityGrant(t, f, original.client)
	// Even another registered public client must not revoke this grant.
	raw, _ := original.b.request("POST", "/oauth/register", map[string]any{"redirect_uris": []string{"https://other.invalid/callback"}}, nil, 201)
	wrongClient := decode(t, raw)["client_id"].(string)
	original.b.request("POST", "/oauth/revoke", url.Values{"client_id": {wrongClient}, "token": {original.refresh}}, nil, 200)
	wrongForm := original.form()
	wrongForm.Set("client_id", wrongClient)
	original.b.request("POST", "/oauth/token", wrongForm, nil, 400)
	original.profile(200)
	successor := original.rotate(t)
	original.b.request("POST", "/oauth/token", original.form(), nil, 400)
	original.profile(401)
	successor.profile(401)
	successor.b.request("POST", "/oauth/token", successor.form(), nil, 400)
	// A separate consent grant for the same account and OAuth client survives.
	other.profile(200)
	other.rotate(t).profile(200)
}

func TestOAuthSecurityRevokeClosesWholeGrant(t *testing.T) {
	for _, kind := range []string{"refresh", "access", "consumed-refresh"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			original := newSecurityGrant(t, f, "")
			current := original.rotate(t)
			token := current.refresh
			if kind == "access" {
				token = current.access
			}
			if kind == "consumed-refresh" {
				token = original.refresh
			}
			form := url.Values{"client_id": {current.client}, "token": {token}}
			current.b.request("POST", "/oauth/revoke", form, nil, 200)
			original.profile(401)
			current.profile(401)
			current.b.request("POST", "/oauth/token", current.form(), nil, 400)
			current.b.request("POST", "/oauth/revoke", form, nil, 200)
			current.b.request("POST", "/oauth/revoke", url.Values{"client_id": {current.client}, "token": {"unknown"}}, nil, 200)
		})
	}
}

func TestOAuthSecurityConcurrentRefreshAndRevoke(t *testing.T) {
	f := setup(t)
	g := newSecurityGrant(t, f, "")
	type response struct {
		status int
		body   []byte
		err    error
	}
	out := make(chan response, 2)
	start := make(chan struct{})
	send := func(path string, form url.Values) {
		<-start
		req, _ := http.NewRequest("POST", f.server.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			out <- response{err: err}
			return
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		out <- response{res.StatusCode, raw, err}
	}
	go send("/oauth/token", g.form())
	go send("/oauth/revoke", url.Values{"client_id": {g.client}, "token": {g.refresh}})
	close(start)
	for range 2 {
		res := <-out
		if res.err != nil || (res.status != 200 && res.status != 400) {
			t.Fatalf("concurrent request: %d %v", res.status, res.err)
		}
		data := decode(t, res.body)
		if token, ok := data["access_token"].(string); ok {
			// Save until both operations have returned before checking validity.
			g.access = token
			g.refresh = data["refresh_token"].(string)
		}
	}
	g.profile(401)
	g.b.request("POST", "/oauth/token", g.form(), nil, 400)
}

func TestOAuthSecurityRegistrationPoolCannotBlockNewClients(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	active := newSecurityGrant(t, f, "")
	prefix := fmt.Sprintf("security-pool-%d-", time.Now().UnixNano())
	_, err := f.pool.Exec(ctx, `INSERT INTO oauth_clients(id,name,redirect_uris,auth_method,created_at) SELECT $1 || n::text,'unused',ARRAY['https://example.invalid/callback'],'none',NOW()-interval '1 hour' FROM generate_series(1,1100) n`, prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.pool.Exec(context.Background(), `DELETE FROM oauth_clients WHERE starts_with(id,$1)`, prefix)
	})
	// A full anonymous pool must admit concurrent new registrations without 503.
	var wg sync.WaitGroup
	failures := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := http.Post(f.server.URL+"/oauth/register", "application/json", strings.NewReader(`{"redirect_uris":["https://example.invalid/callback"]}`))
			if err != nil {
				failures <- err.Error()
				return
			}
			defer res.Body.Close()
			io.Copy(io.Discard, res.Body)
			if res.StatusCode != 201 {
				failures <- fmt.Sprintf("registration returned %d", res.StatusCode)
			}
		}()
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM oauth_clients c WHERE activated_at IS NULL AND NOT EXISTS(SELECT 1 FROM oauth_requests r WHERE r.client_id=c.id) AND NOT EXISTS(SELECT 1 FROM oauth_codes r WHERE r.client_id=c.id) AND NOT EXISTS(SELECT 1 FROM oauth_refresh_tokens r WHERE r.client_id=c.id)`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count > 1000 {
		t.Fatalf("unused pool grew to %d", count)
	}
	active.rotate(t).profile(200)
}

func TestOAuthSecurityUnusedRegistrationExpiry(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	active := newSecurityGrant(t, f, "")
	// A signed-in user may still be deciding on consent when the unused TTL
	// elapses or an anonymous registration burst arrives. Preserve that flow.
	pendingRaw, _ := active.b.request("POST", "/oauth/register", map[string]any{"redirect_uris": []string{"https://example.invalid/callback"}}, nil, 201)
	pending := decode(t, pendingRaw)["client_id"].(string)
	verifier := strings.Repeat("p", 64)
	digest := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {pending}, "redirect_uri": {"https://example.invalid/callback"}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "scope": {"profile:read"}}
	consent, _ := active.b.request("GET", "/oauth/authorize?"+q.Encode(), nil, nil, 200)
	raw, _ := active.b.request("POST", "/oauth/register", map[string]any{"redirect_uris": []string{"https://example.invalid/callback"}}, nil, 201)
	unused := decode(t, raw)["client_id"].(string)
	if _, err := f.pool.Exec(ctx, `UPDATE oauth_clients SET created_at=NOW()-interval '2 days' WHERE id=ANY($1)`, []string{active.client, unused, pending}); err != nil {
		t.Fatal(err)
	}
	// Expiry is enforced even before periodic cleanup.
	active.b.request("GET", "/oauth/authorize?client_id="+url.QueryEscape(unused), nil, nil, 400)
	if err := oauth.CleanupRegistrations(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM oauth_clients WHERE id=$1)`, unused).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("expired unused registration survived cleanup")
	}
	_, h := active.b.request("POST", "/oauth/authorize", url.Values{"request_id": {hidden(t, consent, "name", "request_id")}, "csrf": {hidden(t, consent, "name", "csrf")}, "decision": {"allow"}, "account_id": {f.account}}, nil, 303)
	callback, _ := url.Parse(h.Get("Location"))
	active.b.request("POST", "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {pending}, "code": {callback.Query().Get("code")}, "redirect_uri": {q.Get("redirect_uri")}, "code_verifier": {verifier}}, nil, 200)
	active.rotate(t).profile(200)
}
