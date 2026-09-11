package access

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Principal struct {
	UserID    string
	ClientID  string
	AccountID string
	Scopes    []Scope
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) CreateClient(ctx context.Context, userID, name, clientType string) (string, error) {
	name = strings.TrimSpace(name)
	clientType = strings.ToUpper(strings.TrimSpace(clientType))
	if userID == "" || name == "" {
		return "", errors.New("user id and client name are required")
	}
	if clientType != "MCP" && clientType != "API" {
		return "", errors.New("client type must be MCP or API")
	}

	var id string
	if err := r.pool.QueryRow(ctx, `
		INSERT INTO gateway_clients (user_id, name, client_type)
		VALUES ($1::uuid, $2, $3)
		RETURNING id::text`, userID, name, clientType).Scan(&id); err != nil {
		return "", fmt.Errorf("create gateway client: %w", err)
	}
	return id, nil
}

func (r *Repository) IssueToken(ctx context.Context, clientID, accountID string, scopes []Scope, expiresAt *time.Time) (GeneratedToken, error) {
	normalized, err := NormalizeScopes(scopes)
	if err != nil {
		return GeneratedToken{}, err
	}
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		return GeneratedToken{}, errors.New("token expiry must be in the future")
	}

	token, err := GenerateToken()
	if err != nil {
		return GeneratedToken{}, fmt.Errorf("generate access token: %w", err)
	}

	scopeValues := make([]string, len(normalized))
	for i, scope := range normalized {
		scopeValues[i] = string(scope)
	}

	result, err := r.pool.Exec(ctx, `
		INSERT INTO access_tokens (
			client_id, account_id, token_prefix, token_hash, scopes, expires_at
		)
		SELECT $1::uuid, $2::uuid, $3, $4, $5::text[], $6
		FROM gateway_clients gc
		JOIN telegram_accounts ta ON ta.id = $2::uuid
		WHERE gc.id = $1::uuid
		  AND gc.user_id = ta.user_id
		  AND gc.status = 'active'
		  AND ta.status = 'active'`,
		clientID, accountID, token.Prefix, token.Hash, scopeValues, expiresAt,
	)
	if err != nil {
		return GeneratedToken{}, fmt.Errorf("issue access token: %w", err)
	}
	if result.RowsAffected() != 1 {
		return GeneratedToken{}, errors.New("client cannot access this Telegram account")
	}
	return token, nil
}

func (r *Repository) AuthenticateBearer(ctx context.Context, plaintext string) (Principal, error) {
	plaintext = strings.TrimSpace(plaintext)
	if plaintext == "" {
		return Principal{}, errors.New("bearer token is required")
	}
	if !strings.HasPrefix(plaintext, tokenPrefix) {
		return Principal{}, errors.New("invalid bearer token")
	}

	hash := sha256.Sum256([]byte(plaintext))
	var principal Principal
	var scopeValues []string

	err := r.pool.QueryRow(ctx, `
		SELECT gc.user_id::text, at.client_id::text, at.account_id::text, at.scopes
		FROM access_tokens at
		JOIN gateway_clients gc ON gc.id = at.client_id
		JOIN telegram_accounts ta ON ta.id = at.account_id
		WHERE at.token_hash = $1
		  AND at.revoked_at IS NULL
		  AND (at.expires_at IS NULL OR at.expires_at > NOW())
		  AND gc.status = 'active'
		  AND ta.status = 'active'
		LIMIT 1`, hash[:],
	).Scan(&principal.UserID, &principal.ClientID, &principal.AccountID, &scopeValues)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, errors.New("invalid or expired bearer token")
	}
	if err != nil {
		return Principal{}, fmt.Errorf("authenticate bearer token: %w", err)
	}

	principal.Scopes = make([]Scope, len(scopeValues))
	for i, scope := range scopeValues {
		principal.Scopes[i] = Scope(scope)
	}
	if err := ValidateScopes(principal.Scopes); err != nil {
		return Principal{}, fmt.Errorf("stored token has invalid scopes: %w", err)
	}
	return principal, nil
}

func (r *Repository) RevokeToken(ctx context.Context, tokenID, clientID string) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE access_tokens
		SET revoked_at = COALESCE(revoked_at, NOW())
		WHERE id = $1::uuid AND client_id = $2::uuid`, tokenID, clientID)
	if err != nil {
		return fmt.Errorf("revoke access token: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
