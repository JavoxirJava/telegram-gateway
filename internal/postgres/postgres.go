package postgres

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(ctx context.Context, cfg config.PostgresConfig) (*pgxpool.Pool, error) {
	dsn := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.User, cfg.Password),
		Host:   fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Path:   cfg.Database,
	}
	query := dsn.Query()
	query.Set("sslmode", cfg.SSLMode)
	dsn.RawQuery = query.Encode()

	poolConfig, err := pgxpool.ParseConfig(dsn.String())
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}

	poolConfig.MaxConns = 20
	poolConfig.MinConns = 2
	poolConfig.MaxConnLifetime = 30 * time.Minute
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return pool, nil
}
