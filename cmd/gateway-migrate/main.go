package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"github.com/JavoxirJava/telegram-gateway/internal/config"
	"github.com/JavoxirJava/telegram-gateway/internal/postgres"
	"github.com/JavoxirJava/telegram-gateway/migrations"
	"github.com/jackc/pgx/v5"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	adopt := flag.Int("adopt-through", 0, "explicitly record already-applied migrations up to this version on a legacy database")
	flag.Parse()
	if flag.NArg() != 0 || *adopt < 0 {
		return errors.New("invalid migration arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg, e := config.Load()
	if e != nil {
		return e
	}
	p, e := postgres.Open(ctx, cfg.Postgres)
	if e != nil {
		return errors.New("migration database unavailable")
	}
	defer p.Close()
	conn, e := p.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	if _, e = conn.Exec(ctx, `SELECT pg_advisory_lock(718240712)`); e != nil {
		return e
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(c, `SELECT pg_advisory_unlock(718240712)`)
	}()
	if _, e = conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS gateway_schema_migrations(version integer PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); e != nil {
		return e
	}
	var count int
	if e = conn.QueryRow(ctx, `SELECT count(*) FROM gateway_schema_migrations`).Scan(&count); e != nil {
		return e
	}
	if *adopt > 0 && count > 0 {
		return errors.New("adoption is only allowed on an untracked legacy database")
	}
	names, e := migrations.Files.ReadDir(".")
	if e != nil {
		return e
	}
	files := []string{}
	for _, v := range names {
		if strings.HasSuffix(v.Name(), ".up.sql") {
			files = append(files, v.Name())
		}
	}
	sort.Strings(files)
	if *adopt > len(files) {
		return errors.New("adopt-through exceeds embedded schema")
	}
	for _, name := range files {
		version, e := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if e != nil {
			return e
		}
		raw, e := migrations.Files.ReadFile(name)
		if e != nil {
			return e
		}
		hash := sha256.Sum256(raw)
		sum := hex.EncodeToString(hash[:])
		var existing string
		e = conn.QueryRow(ctx, `SELECT checksum FROM gateway_schema_migrations WHERE version=$1`, version).Scan(&existing)
		if e == nil {
			if existing != sum {
				return fmt.Errorf("applied migration %s was changed", name)
			}
			continue
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		sql := strings.TrimSpace(string(raw))
		if !strings.HasPrefix(sql, "BEGIN;") || !strings.HasSuffix(sql, "COMMIT;") {
			return fmt.Errorf("migration %s has no expected transaction wrapper", name)
		}
		sql = strings.TrimSuffix(strings.TrimPrefix(sql, "BEGIN;"), "COMMIT;")
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		if version > *adopt {
			_, e = tx.Exec(ctx, sql)
		}
		if e == nil {
			_, e = tx.Exec(ctx, `INSERT INTO gateway_schema_migrations(version,checksum) VALUES($1,$2)`, version, sum)
		}
		if e != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s failed: %w", name, e)
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		fmt.Println("Applied", name)
	}
	return nil
}
