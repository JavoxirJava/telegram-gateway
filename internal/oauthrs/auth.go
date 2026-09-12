// Package oauthrs is an OAuth protected-resource boundary. The external identity
// provider owns login, consent, S256 PKCE, token rotation and revocation. Never
// implement password/authorization-code grants inside a Telegram read gateway.
package oauthrs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
)

var ErrUnavailable = errors.New("authorization service unavailable")

type Resolver interface {
	Resolve(context.Context, string, string, string) (access.Principal, error)
}
type Config struct {
	Issuer, Resource, ClientID, ClientSecret string
	Client                                   *http.Client
}
type Authenticator struct {
	cfg      Config
	endpoint string
	client   *http.Client
	resolver Resolver
}
type discovery struct {
	Issuer        string   `json:"issuer"`
	Introspection string   `json:"introspection_endpoint"`
	Authorization string   `json:"authorization_endpoint"`
	Token         string   `json:"token_endpoint"`
	PKCE          []string `json:"code_challenge_methods_supported"`
	ResponseTypes []string `json:"response_types_supported"`
}

func HTTPSURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Opaque == ""
}
func New(ctx context.Context, cfg Config, resolver Resolver) (*Authenticator, error) {
	if !HTTPSURL(cfg.Issuer) || !HTTPSURL(cfg.Resource) || strings.HasSuffix(cfg.Issuer, "/") || cfg.ClientID == "" || cfg.ClientSecret == "" || resolver == nil {
		return nil, errors.New("OAuth issuer/resource HTTPS URLs and introspection credentials are required")
	}
	client := &http.Client{Timeout: 8 * time.Second}
	if cfg.Client != nil {
		copy := *cfg.Client
		client = &copy
		client.Timeout = 8 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("authorization redirects are forbidden") }
	req, err := http.NewRequestWithContext(ctx, "GET", cfg.Issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()
	var d discovery
	if resp.StatusCode != 200 || decodeBounded(resp.Body, &d) != nil {
		return nil, ErrUnavailable
	}
	issuer, _ := url.Parse(cfg.Issuer)
	for _, v := range []string{d.Introspection, d.Authorization, d.Token} {
		u, _ := url.Parse(v)
		if !HTTPSURL(v) || u.Host != issuer.Host {
			return nil, errors.New("OAuth endpoints must use the configured issuer origin")
		}
	}
	if d.Issuer != cfg.Issuer || !slices.Contains(d.PKCE, "S256") || !slices.Contains(d.ResponseTypes, "code") {
		return nil, errors.New("issuer discovery must match and advertise authorization-code/S256 PKCE")
	}
	return &Authenticator{cfg: cfg, client: client, endpoint: d.Introspection, resolver: resolver}, nil
}
func decodeBounded(reader io.Reader, v any) error {
	b, e := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
	if e != nil || len(b) > 64<<10 {
		return ErrUnavailable
	}
	return json.Unmarshal(b, v)
}

type tokenInfo struct {
	Active    bool            `json:"active"`
	Issuer    string          `json:"iss"`
	Subject   string          `json:"sub"`
	ClientID  string          `json:"client_id"`
	Scope     string          `json:"scope"`
	Audience  json.RawMessage `json:"aud"`
	Expires   int64           `json:"exp"`
	NotBefore int64           `json:"nbf"`
	TokenType string          `json:"token_type"`
}

// No positive-result cache: revocation and grant changes are checked each time.
// Credentials go only to the statically discovered trusted issuer, never to AI
// clients, arbitrary URLs, NATS, MinIO or Telegram.
func (a *Authenticator) AuthenticateBearer(ctx context.Context, token string) (access.Principal, error) {
	if token == "" || len(token) > 16384 || strings.ContainsAny(token, "\r\n\t ") {
		return access.Principal{}, access.ErrInvalidBearer
	}
	form := url.Values{"token": {token}, "token_type_hint": {"access_token"}}
	req, err := http.NewRequestWithContext(ctx, "POST", a.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return access.Principal{}, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(a.cfg.ClientID), url.QueryEscape(a.cfg.ClientSecret))
	resp, err := a.client.Do(req)
	if err != nil {
		return access.Principal{}, ErrUnavailable
	}
	defer resp.Body.Close()
	var v tokenInfo
	if resp.StatusCode != 200 || decodeBounded(resp.Body, &v) != nil {
		return access.Principal{}, ErrUnavailable
	}
	now := time.Now().Unix()
	if !v.Active || v.Issuer != a.cfg.Issuer || v.Subject == "" || len(v.Subject) > 512 || v.ClientID == "" || len(v.ClientID) > 256 || v.Expires <= now || v.NotBefore > now || !strings.EqualFold(v.TokenType, "Bearer") || !hasAudience(v.Audience, a.cfg.Resource) {
		return access.Principal{}, access.ErrInvalidBearer
	}
	p, err := a.resolver.Resolve(ctx, a.cfg.Issuer, v.Subject, v.ClientID)
	if err != nil {
		return access.Principal{}, err
	}
	granted := strings.Fields(v.Scope)
	out := make([]access.Scope, 0, len(p.Scopes))
	for _, s := range p.Scopes {
		if slices.Contains(granted, string(s)) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return access.Principal{}, access.ErrInvalidBearer
	}
	if err = access.ValidateScopes(out); err != nil {
		return access.Principal{}, access.ErrInvalidBearer
	}
	p.Scopes = out
	return p, nil
}
func hasAudience(raw json.RawMessage, want string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want
	}
	var many []string
	return json.Unmarshal(raw, &many) == nil && slices.Contains(many, want)
}

// Combined permits explicitly issued gateway API keys only on REST routes.
// MCP must be wired directly to the OAuth authenticator, never this fallback.
type Combined struct {
	OAuth *Authenticator
	API   *access.Repository
}

func (c Combined) AuthenticateBearer(ctx context.Context, t string) (access.Principal, error) {
	if strings.HasPrefix(t, "tgw_") {
		return c.API.AuthenticateBearer(ctx, t)
	}
	if c.OAuth != nil {
		return c.OAuth.AuthenticateBearer(ctx, t)
	}
	return c.API.AuthenticateBearer(ctx, t)
}
