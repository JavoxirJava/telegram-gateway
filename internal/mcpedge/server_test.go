package mcpedge

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type authFake struct {
	scopes           []access.Scope
	calls, denyAfter int
}

func (a *authFake) AuthenticateBearer(_ context.Context, t string) (access.Principal, error) {
	a.calls++
	if t != "test-bearer" || (a.denyAfter > 0 && a.calls > a.denyAfter) {
		return access.Principal{}, access.ErrInvalidBearer
	}
	return access.Principal{UserID: "owner", ClientID: "client", AccountID: "account", Scopes: a.scopes}, nil
}
func fixture(t *testing.T, scopes ...access.Scope) (http.Handler, *authFake, *int) {
	t.Helper()
	a := &authFake{scopes: scopes}
	calls := new(int)
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-bearer" {
			t.Error("unexpected internal call")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"content":"private fixture"}]}`)
	})
	h, e := New(Config{PublicURL: "https://gateway.test/mcp", Issuer: "https://issuer.test", AllowedOrigins: []string{"https://approved.test"}, Limit: func(context.Context, access.Principal) (time.Duration, error) { return 0, nil }}, a, api)
	if e != nil {
		t.Fatal(e)
	}
	return h, a, calls
}
func request(body string) *http.Request {
	r := httptest.NewRequest("POST", "https://gateway.test/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", "2025-11-25")
	r.Header.Set("Authorization", "Bearer test-bearer")
	return r
}
func TestMissingBearerGetsDiscoveryChallenge(t *testing.T) {
	h, _, _ := fixture(t, access.ScopeProfileRead)
	r := request(`{}`)
	r.Header.Del("Authorization")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "oauth-protected-resource/mcp") {
		t.Fatal(w.Code, w.Header())
	}
}
func TestScopeDeniedBeforeDatabase(t *testing.T) {
	h, _, calls := fixture(t, access.ScopeProfileRead)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_messages","arguments":{"chat_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}}}`))
	if w.Code != 403 || *calls != 0 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "messages:read") {
		t.Fatal(w.Code, *calls, w.Body.String())
	}
}
func TestSDKToolCallReadsAuditedAPI(t *testing.T) {
	h, _, calls := fixture(t, access.ScopeProfileRead)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_profile","arguments":{}}}`))
	if w.Code != 200 || *calls != 1 || !strings.Contains(w.Body.String(), "private fixture") {
		t.Fatal(w.Code, *calls, w.Body.String())
	}
}
func TestRevocationDuringReadDoesNotExposeResult(t *testing.T) {
	h, a, _ := fixture(t, access.ScopeProfileRead)
	a.denyAfter = 1
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_profile","arguments":{}}}`))
	if strings.Contains(w.Body.String(), "private fixture") || !strings.Contains(w.Body.String(), "authorization changed") {
		t.Fatal(w.Body.String())
	}
}
func TestWriteAndCrossAccountArgumentsRejected(t *testing.T) {
	for _, body := range []string{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_message","arguments":{}}}`, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_profile","arguments":{"account_id":"someone-else"}}}`} {
		h, _, calls := fixture(t, access.ScopeProfileRead)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request(body))
		if *calls != 0 || strings.Contains(w.Body.String(), "private fixture") {
			t.Fatal(w.Body.String())
		}
	}
}
func TestOriginsHostAndRequestBounds(t *testing.T) {
	for _, kind := range []string{"origin", "host", "oversize", "query", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			h, _, calls := fixture(t, access.ScopeProfileRead)
			r := request(`{}`)
			switch kind {
			case "origin":
				r.Header.Set("Origin", "https://attacker.test")
			case "host":
				r.Host = "attacker.test"
			case "oversize":
				r = request(strings.Repeat("a", MaxRequest+1))
			case "query":
				r.URL.RawQuery = "access_token=secret"
			case "duplicate":
				r.Header.Add("Authorization", "Bearer second")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code < 400 || *calls > 0 {
				t.Fatal(kind, w.Code, *calls)
			}
		})
	}
}
func TestOnlyReadToolsAdvertised(t *testing.T) {
	h, _, _ := fixture(t, access.ScopeProfileRead)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	var out struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Annotations struct {
					Read bool `json:"readOnlyHint"`
				} `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Result.Tools) != len(tools) {
		t.Fatal(w.Body.String())
	}
	for _, v := range out.Result.Tools {
		if !v.Annotations.Read {
			t.Fatal(v)
		}
	}
}
func TestResponseWriterBounds(t *testing.T) {
	w := &boundedResponse{header: make(http.Header)}
	if _, e := w.Write(make([]byte, MaxResponse+1)); e == nil || !w.overflow || w.body.Len() != 0 {
		t.Fatal("unbounded result")
	}
}
