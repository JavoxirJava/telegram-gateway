// gatewayctl is an operator-only CLI. It is not a public HTTP control plane.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/config"
	"github.com/JavoxirJava/telegram-gateway/internal/operator"
	"github.com/JavoxirJava/telegram-gateway/internal/postgres"
	"github.com/JavoxirJava/telegram-gateway/internal/readmodel"
	"os"
	"strings"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "gatewayctl failed:", e)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("commands: provision, grant, revoke, retry, status; use <command> -h")
	}
	command := os.Args[1]
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	actor := f.String("operator", "", "named authorized operator (required)")
	user := f.String("user", "", "existing app user UUID (provision only; empty creates one)")
	account := f.String("account", "", "Telegram gateway account UUID")
	issuer := f.String("issuer", "", "verified OAuth issuer HTTPS URL")
	subject := f.String("subject", "", "verified immutable OAuth subject, never an email/name")
	client := f.String("client", "", "registered external OAuth client ID")
	consent := f.String("consent", "", "reference to informed approval record")
	scope := f.String("scopes", "profile:read,chats:list", "approved comma-separated read-only scopes")
	job := f.String("job", "", "dead sync job UUID (retry only)")
	if e := f.Parse(os.Args[2:]); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if strings.TrimSpace(*actor) == "" {
		return errors.New("--operator is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg, e := config.Load()
	if e != nil {
		return e
	}
	pool, e := postgres.Open(ctx, cfg.Postgres)
	if e != nil {
		return errors.New("operator database connection failed")
	}
	defer pool.Close()
	service := operator.Service{Pool: pool, Actor: *actor}
	var result any
	switch command {
	case "provision":
		result, e = service.Provision(ctx, *user)
	case "grant":
		ss := []access.Scope{}
		for _, s := range strings.Split(*scope, ",") {
			ss = append(ss, access.Scope(strings.TrimSpace(s)))
		}
		var id string
		id, e = service.Grant(ctx, operator.Grant{Issuer: *issuer, Subject: *subject, OAuthClient: *client, AccountID: *account, ConsentReference: *consent, Scopes: ss})
		result = map[string]string{"gateway_client_id": id}
	case "revoke":
		e = service.Revoke(ctx, *account, *issuer, *subject, *client)
		result = map[string]bool{"revoked": e == nil}
	case "retry":
		e = service.Retry(ctx, *account, *job)
		result = map[string]bool{"queued": e == nil}
	case "status":
		result, e = (&readmodel.Repository{Pool: pool}).Status(ctx, *account)
	default:
		return errors.New("unknown command")
	}
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
