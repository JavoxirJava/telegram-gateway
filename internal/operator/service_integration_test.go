package operator

import (
	"context"
	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/oauthrs"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"time"
)

func TestGrantOwnershipRevocationAndScopeConstraints(t *testing.T) {
	d := os.Getenv("TEST_DATABASE_URL")
	if d == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, e := pgxpool.New(ctx, d)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	s := Service{Pool: p, Actor: "fixture"}
	one, e := s.Provision(ctx, "")
	if e != nil {
		t.Fatal(e)
	}
	two, e := s.Provision(ctx, "")
	if e != nil {
		t.Fatal(e)
	}
	g := Grant{Issuer: "https://identity.test/realm", Subject: one.UserID, OAuthClient: "ai", AccountID: one.AccountID, ConsentReference: "fixture-consent", Scopes: []access.Scope{access.ScopeProfileRead}}
	if _, e = s.Grant(ctx, g); e == nil {
		t.Fatal("pending account granted")
	}
	if _, e = p.Exec(ctx, `UPDATE telegram_accounts SET status='active' WHERE id=ANY($1::uuid[])`, []string{one.AccountID, two.AccountID}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Grant(ctx, g); e != nil {
		t.Fatal(e)
	}
	r := oauthrs.Grants{Pool: p}
	v, e := r.Resolve(ctx, g.Issuer, g.Subject, g.OAuthClient)
	if e != nil || v.AccountID != one.AccountID {
		t.Fatal(v, e)
	}
	g.AccountID = two.AccountID
	if _, e = s.Grant(ctx, g); e == nil {
		t.Fatal("cross-user grant accepted")
	}
	g.AccountID = one.AccountID
	if _, e = p.Exec(ctx, `UPDATE gateway_oauth_grants SET scopes=ARRAY['messages:send'] WHERE issuer=$1 AND subject=$2`, g.Issuer, g.Subject); e == nil {
		t.Fatal("write scope in database")
	}
	if e = s.Revoke(ctx, g.AccountID, g.Issuer, g.Subject, g.OAuthClient); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Resolve(ctx, g.Issuer, g.Subject, g.OAuthClient); e == nil {
		t.Fatal("revoked grant accepted")
	}
}
