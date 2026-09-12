package oauthrs

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type resolverFake struct{ calls int }

func (r *resolverFake) Resolve(_ context.Context, issuer, sub, client string) (access.Principal, error) {
	r.calls++
	if issuer != "https://issuer.test" || sub != "owner" || client != "ai-client" {
		return access.Principal{}, access.ErrInvalidBearer
	}
	return access.Principal{UserID: "u", ClientID: "c", AccountID: "a", ClientType: "MCP", Scopes: []access.Scope{access.ScopeProfileRead, access.ScopeMessagesRead}}, nil
}
func oauthFixture(t *testing.T, token *map[string]any) (*Authenticator, *resolverFake) {
	t.Helper()
	resolver := &resolverFake{}
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		var value any
		if r.Method == "GET" {
			value = map[string]any{"issuer": "https://issuer.test", "introspection_endpoint": "https://issuer.test/introspect", "authorization_endpoint": "https://issuer.test/authorize", "token_endpoint": "https://issuer.test/token", "code_challenge_methods_supported": []string{"S256"}, "response_types_supported": []string{"code"}}
		} else {
			r.ParseForm()
			if r.URL.String() != "https://issuer.test/introspect" || r.Form.Get("token") != "sample-token" {
				t.Error("token sent to unexpected endpoint")
			}
			u, p, ok := r.BasicAuth()
			if !ok || u != "resource" || p != "server-secret" {
				t.Error("introspection not authenticated")
			}
			value = *token
		}
		b, _ := json.Marshal(value)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b))), Header: make(http.Header)}, nil
	})}
	a, e := New(context.Background(), Config{Issuer: "https://issuer.test", Resource: "https://gateway.test/mcp", ClientID: "resource", ClientSecret: "server-secret", Client: client}, resolver)
	if e != nil {
		t.Fatal(e)
	}
	return a, resolver
}
func validToken() map[string]any {
	return map[string]any{"active": true, "iss": "https://issuer.test", "sub": "owner", "client_id": "ai-client", "scope": "messages:read media:read", "aud": "https://gateway.test/mcp", "exp": time.Now().Add(time.Minute).Unix(), "token_type": "Bearer"}
}
func TestTokenScopesAreIntersection(t *testing.T) {
	v := validToken()
	a, r := oauthFixture(t, &v)
	p, e := a.AuthenticateBearer(context.Background(), "sample-token")
	if e != nil || len(p.Scopes) != 1 || p.Scopes[0] != access.ScopeMessagesRead || r.calls != 1 {
		t.Fatal(p, e)
	}
}
func TestTokenValidationRejectsInvalidClaims(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		value     any
	}{{"inactive", "active", false}, {"expiry", "exp", 1}, {"issuer", "iss", "https://attacker.test"}, {"audience", "aud", "another-resource"}, {"empty subject", "sub", ""}, {"wrong client", "client_id", "other"}, {"id token", "token_type", "ID"}, {"future", "nbf", time.Now().Add(time.Hour).Unix()}, {"empty scope", "scope", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			v := validToken()
			v[tc.key] = tc.value
			a, _ := oauthFixture(t, &v)
			_, e := a.AuthenticateBearer(context.Background(), "sample-token")
			if !errors.Is(e, access.ErrInvalidBearer) {
				t.Fatal("invalid token accepted", e)
			}
		})
	}
}
func TestRevocationNotCached(t *testing.T) {
	v := validToken()
	a, _ := oauthFixture(t, &v)
	if _, e := a.AuthenticateBearer(context.Background(), "sample-token"); e != nil {
		t.Fatal(e)
	}
	v["active"] = false
	if _, e := a.AuthenticateBearer(context.Background(), "sample-token"); !errors.Is(e, access.ErrInvalidBearer) {
		t.Fatal("revoked token accepted", e)
	}
}
func TestAudienceStringOrArray(t *testing.T) {
	for _, raw := range []string{`"resource"`, `["another","resource"]`} {
		if !hasAudience(json.RawMessage(raw), "resource") {
			t.Fatal(raw)
		}
	}
	for _, raw := range []string{`null`, `123`, `[123]`, `"other"`} {
		if hasAudience(json.RawMessage(raw), "resource") {
			t.Fatal(raw)
		}
	}
}
func TestHTTPSConfigurationRejectsCredentialURLs(t *testing.T) {
	for _, s := range []string{"http://issuer.test", "https://user:password@issuer.test", "https://issuer.test?token=x", "https://issuer.test/#fragment", "javascript:alert(1)"} {
		if HTTPSURL(s) {
			t.Fatal(s)
		}
	}
}
func TestOversizedIntrospectionRejected(t *testing.T) {
	var target any
	if decodeBounded(strings.NewReader(strings.Repeat(" ", 65<<10)), &target) == nil {
		t.Fatal("unbounded introspection")
	}
}
