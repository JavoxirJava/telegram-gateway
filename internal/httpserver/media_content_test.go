package httpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	"github.com/JavoxirJava/telegram-gateway/internal/media"
)

type mediaAuthFake struct{ principal access.Principal }

func (a mediaAuthFake) AuthenticateBearer(context.Context, string) (access.Principal, error) {
	return a.principal, nil
}

type auditFake struct {
	fail   bool
	writes int
}

func (a *auditFake) Write(context.Context, audit.Event) error {
	a.writes++
	if a.fail {
		return errors.New("unavailable")
	}
	return nil
}

type mediaFake struct{ opens int }

func (m *mediaFake) Open(ctx context.Context, _, _ string, authorize func(context.Context) error) (media.Content, error) {
	m.opens++
	if err := authorize(ctx); err != nil {
		return media.Content{}, err
	}
	return media.Content{Reader: io.NopCloser(strings.NewReader("bytes")), Size: 5, FileName: "../../evil.html"}, nil
}
func httpMediaFixture() (*Server, *mediaFake, *auditFake, access.Principal) {
	p := access.Principal{UserID: "user", ClientID: "client", AccountID: "account", Scopes: []access.Scope{access.ScopeMediaRead}}
	m := &mediaFake{}
	a := &auditFake{}
	return &Server{access: mediaAuthFake{p}, audit: a, media: m, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, m, a, p
}
func mediaRequest(p access.Principal) *http.Request {
	r := httptest.NewRequest("GET", "/v1/media/a/content", nil)
	r.SetPathValue("mediaID", "11111111-1111-4111-8111-111111111111")
	r.Header.Set("Authorization", "Bearer test")
	return r.WithContext(context.WithValue(r.Context(), principalContextKey{}, p))
}
func TestMediaHTTPAuditBeforeBody(t *testing.T) {
	s, _, a, p := httpMediaFixture()
	a.fail = true
	w := httptest.NewRecorder()
	s.mediaContent(w, mediaRequest(p))
	if w.Code != 503 || strings.Contains(w.Body.String(), "bytes") {
		t.Fatalf("audit bypass: %d %s", w.Code, w.Body.String())
	}
}
func TestMediaHTTPDownloadHeaders(t *testing.T) {
	s, _, a, p := httpMediaFixture()
	w := httptest.NewRecorder()
	s.mediaContent(w, mediaRequest(p))
	if w.Code != 200 || w.Body.String() != "bytes" || a.writes != 1 {
		t.Fatal(w.Code, w.Body.String(), a.writes)
	}
	if w.Header().Get("Content-Type") != "application/octet-stream" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") || strings.Contains(w.Header().Get("Content-Disposition"), "../") {
		t.Fatal(w.Header())
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("media is cacheable")
	}
}
func TestMediaHTTPReadScopeRequired(t *testing.T) {
	s, m, _, p := httpMediaFixture()
	p.Scopes = []access.Scope{access.ScopeMessagesRead}
	w := httptest.NewRecorder()
	s.mediaContent(w, mediaRequest(p))
	if w.Code != 403 || m.opens != 0 {
		t.Fatal(w.Code, m.opens)
	}
}
func TestLegacyMediaURLRetired(t *testing.T) {
	s, m, _, p := httpMediaFixture()
	w := httptest.NewRecorder()
	s.mediaReadURL(w, mediaRequest(p))
	if w.Code != 410 || m.opens != 0 {
		t.Fatal(w.Code, m.opens)
	}
}
func TestMediaHTTPNoRangesOrAnonymousHead(t *testing.T) {
	s, _, _, p := httpMediaFixture()
	r := mediaRequest(p)
	r.Header.Set("Range", "bytes=0-1")
	w := httptest.NewRecorder()
	s.mediaContent(w, r)
	if w.Code != 416 || w.Header().Get("Content-Range") != "bytes */5" {
		t.Fatal(w.Code, w.Header())
	}
	r = mediaRequest(p)
	r.Method = "HEAD"
	w = httptest.NewRecorder()
	s.mediaContent(w, r)
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	r = mediaRequest(p)
	r.Header.Del("Authorization")
	w = httptest.NewRecorder()
	s.mediaContent(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}
