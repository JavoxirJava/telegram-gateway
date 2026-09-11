package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	"github.com/JavoxirJava/telegram-gateway/internal/config"
	"github.com/JavoxirJava/telegram-gateway/internal/postgres"
)

func main() {
	chainKey := flag.String("chain", "", "verify one audit chain key; empty verifies all chains")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fail("load configuration", err)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(rootCtx, 2*time.Minute)
	defer cancel()

	pool, err := postgres.Open(ctx, cfg.Postgres)
	if err != nil {
		fail("connect postgres", err)
	}
	defer pool.Close()

	verifier := audit.NewVerifier(pool)
	if strings.TrimSpace(*chainKey) != "" {
		result, err := verifier.VerifyChain(ctx, *chainKey)
		if err != nil {
			fail("verify audit chain", err)
		}
		printJSON(result)
		return
	}

	results, err := verifier.VerifyAll(ctx)
	if err != nil {
		fail("verify audit chains", err)
	}
	printJSON(map[string]any{
		"verified": true,
		"chains":   results,
		"count":    len(results),
	})
}

func printJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fail("encode result", err)
	}
}

func fail(operation string, err error) {
	_, _ = fmt.Fprintf(os.Stderr, "%s: %v\n", operation, err)
	os.Exit(1)
}
