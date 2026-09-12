// Package operator contains privileged, explicit provisioning commands. It is
// never mounted on the public API. Run the CLI only as an authorized operator.
package operator

import (
	"context"
	"errors"
	"strings"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	"github.com/JavoxirJava/telegram-gateway/internal/oauthrs"
	sessionruntime "github.com/JavoxirJava/telegram-gateway/internal/runtime"
	"github.com/JavoxirJava/telegram-gateway/internal/sessionkey"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	Pool  *pgxpool.Pool
	Actor string
}
type Provisioned struct {
	UserID    string `json:"user_id"`
	AccountID string `json:"account_id"`
}
type Grant struct {
	Issuer, Subject, OAuthClient, AccountID, ConsentReference string
	Scopes                                                    []access.Scope
}

func (s Service) transaction(ctx context.Context, fn func(pgx.Tx) error) error {
	if s.Pool == nil || strings.TrimSpace(s.Actor) == "" || len(s.Actor) > 120 {
		return errors.New("named operator and database required")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, `SET LOCAL statement_timeout='10s'; SET LOCAL lock_timeout='5s'`); e != nil {
		return e
	}
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s Service) event(ctx context.Context, tx pgx.Tx, account, action, resource string, metadata map[string]any) error {
	return audit.NewWriter(s.Pool).WriteTx(ctx, tx, audit.Event{AccountID: &account, ActorType: audit.ActorAdmin, ActorID: s.Actor, Action: action, ResourceType: "account", ResourceID: resource, Metadata: metadata})
}

// Provision creates an unconnected account. Authentication/identity verification
// by the native agent, not this command, is required to activate the account.
func (s Service) Provision(ctx context.Context, user string) (Provisioned, error) {
	result := Provisioned{UserID: user}
	if user != "" && !sessionkey.ValidAccountID(user) {
		return result, errors.New("invalid user id")
	}
	e := s.transaction(ctx, func(tx pgx.Tx) error {
		if result.UserID == "" {
			if e := tx.QueryRow(ctx, `INSERT INTO app_users DEFAULT VALUES RETURNING id::text`).Scan(&result.UserID); e != nil {
				return e
			}
		}
		if e := tx.QueryRow(ctx, `INSERT INTO telegram_accounts(user_id,status) VALUES($1::uuid,'pending') RETURNING id::text`, result.UserID).Scan(&result.AccountID); e != nil {
			return e
		}
		return s.event(ctx, tx, result.AccountID, "ACCOUNT_PROVISIONED", result.AccountID, nil)
	})
	return result, e
}
func (s Service) Grant(ctx context.Context, g Grant) (string, error) {
	if !oauthrs.HTTPSURL(g.Issuer) || strings.HasSuffix(g.Issuer, "/") || g.Subject == "" || len(g.Subject) > 512 || g.OAuthClient == "" || len(g.OAuthClient) > 256 || !sessionkey.ValidAccountID(g.AccountID) || strings.TrimSpace(g.ConsentReference) == "" || len(g.ConsentReference) > 512 {
		return "", errors.New("verified issuer/subject/client/account and consent reference required")
	}
	scopes, e := access.NormalizeScopes(g.Scopes)
	if e != nil {
		return "", e
	}
	values := []string{}
	for _, v := range scopes {
		values = append(values, string(v))
	}
	var client string
	e = s.transaction(ctx, func(tx pgx.Tx) error {
		var user string
		if e := tx.QueryRow(ctx, `SELECT user_id::text FROM telegram_accounts WHERE id=$1::uuid AND status='active' FOR SHARE`, g.AccountID).Scan(&user); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO gateway_oauth_subjects(issuer,subject,user_id) VALUES($1,$2,$3::uuid) ON CONFLICT(issuer,subject) DO NOTHING`, g.Issuer, g.Subject, user); e != nil {
			return e
		}
		var bound string
		if e := tx.QueryRow(ctx, `SELECT user_id::text FROM gateway_oauth_subjects WHERE issuer=$1 AND subject=$2 FOR SHARE`, g.Issuer, g.Subject).Scan(&bound); e != nil {
			return e
		}
		if bound != user {
			return errors.New("subject is already bound to another user")
		}
		// A grant's account binding never changes. Use another external OAuth client
		// for a second account; no ambiguous model-selectable account switching.
		var existing string
		e := tx.QueryRow(ctx, `SELECT account_id::text,gateway_client_id::text FROM gateway_oauth_grants WHERE issuer=$1 AND subject=$2 AND oauth_client_id=$3 FOR UPDATE`, g.Issuer, g.Subject, g.OAuthClient).Scan(&existing, &client)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if e == nil && existing != g.AccountID {
			return errors.New("grant account binding cannot be changed")
		}
		if errors.Is(e, pgx.ErrNoRows) {
			if e := tx.QueryRow(ctx, `INSERT INTO gateway_clients(user_id,name,client_type) VALUES($1::uuid,'Approved OAuth integration','MCP') RETURNING id::text`, user).Scan(&client); e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `INSERT INTO gateway_oauth_grants(issuer,subject,oauth_client_id,user_id,account_id,gateway_client_id,scopes,consent_reference) VALUES($1,$2,$3,$4::uuid,$5::uuid,$6::uuid,$7,$8)`, g.Issuer, g.Subject, g.OAuthClient, user, g.AccountID, client, values, g.ConsentReference)
		} else {
			_, e = tx.Exec(ctx, `UPDATE gateway_oauth_grants SET scopes=$4,consent_reference=$5,revoked_at=NULL WHERE issuer=$1 AND subject=$2 AND oauth_client_id=$3`, g.Issuer, g.Subject, g.OAuthClient, values, g.ConsentReference)
		}
		if e != nil {
			return e
		}
		return s.event(ctx, tx, g.AccountID, "OAUTH_GRANT_APPROVED", client, map[string]any{"scopes": values, "consent_reference": g.ConsentReference})
	})
	return client, e
}
func (s Service) Revoke(ctx context.Context, account, issuer, subject, client string) error {
	if !sessionkey.ValidAccountID(account) {
		return errors.New("invalid account")
	}
	return s.transaction(ctx, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE gateway_oauth_grants SET revoked_at=COALESCE(revoked_at,NOW()) WHERE account_id=$1::uuid AND issuer=$2 AND subject=$3 AND oauth_client_id=$4`, account, issuer, subject, client)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		return s.event(ctx, tx, account, "OAUTH_GRANT_REVOKED", account, nil)
	})
}
func (s Service) Retry(ctx context.Context, account, job string) error {
	if !sessionkey.ValidAccountID(account) || !sessionkey.ValidAccountID(job) {
		return errors.New("invalid account or job")
	}
	return s.transaction(ctx, func(tx pgx.Tx) error {
		if e := sessionruntime.LockAccountTx(ctx, tx, account); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE gateway_sync_jobs j SET status='pending',attempts=0,due_at=NOW(),last_error_code=NULL,updated_at=NOW() FROM telegram_session_runtime r WHERE j.account_id=$1::uuid AND j.id=$2::uuid AND j.status='dead' AND r.account_id=j.account_id AND r.generation=j.generation AND r.desired_state='online'`, account, job)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return errors.New("job is not dead in the current account generation")
		}
		return s.event(ctx, tx, account, "SYNC_JOB_RETRIED", job, nil)
	})
}
