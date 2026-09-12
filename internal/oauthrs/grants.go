package oauthrs

import (
	"context"
	"errors"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Grants struct{ Pool *pgxpool.Pool }

func (g Grants) Resolve(ctx context.Context, issuer, subject, client string) (access.Principal, error) {
	var p access.Principal
	var scopes []string
	err := g.Pool.QueryRow(ctx, `SELECT g.user_id::text,g.gateway_client_id::text,g.account_id::text,g.scopes
 FROM gateway_oauth_grants g JOIN telegram_accounts a ON a.id=g.account_id AND a.user_id=g.user_id
 JOIN gateway_clients c ON c.id=g.gateway_client_id AND c.user_id=g.user_id
 WHERE g.issuer=$1 AND g.subject=$2 AND g.oauth_client_id=$3 AND g.revoked_at IS NULL
 AND a.status='active' AND c.status='active'`, issuer, subject, client).Scan(&p.UserID, &p.ClientID, &p.AccountID, &scopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, access.ErrInvalidBearer
	}
	if err != nil {
		return p, ErrUnavailable
	}
	p.ClientType = "MCP"
	for _, s := range scopes {
		p.Scopes = append(p.Scopes, access.Scope(s))
	}
	return p, nil
}
