package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/accounts"
	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	"github.com/JavoxirJava/telegram-gateway/internal/chats"
	"github.com/JavoxirJava/telegram-gateway/internal/config"
	"github.com/JavoxirJava/telegram-gateway/internal/health"
	"github.com/JavoxirJava/telegram-gateway/internal/httpserver"
	"github.com/JavoxirJava/telegram-gateway/internal/media"
	"github.com/JavoxirJava/telegram-gateway/internal/messages"
	"github.com/JavoxirJava/telegram-gateway/internal/natsbus"
	"github.com/JavoxirJava/telegram-gateway/internal/objectstore"
	"github.com/JavoxirJava/telegram-gateway/internal/postgres"
	"github.com/JavoxirJava/telegram-gateway/internal/ratelimit"
	"github.com/JavoxirJava/telegram-gateway/internal/redisstore"
	"github.com/JavoxirJava/telegram-gateway/internal/syncjob"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	startupCtx, cancelStartup := context.WithTimeout(ctx, 20*time.Second)
	defer cancelStartup()

	pool, err := postgres.Open(startupCtx, cfg.Postgres)
	if err != nil {
		logger.Error("failed to connect to postgres", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	redisClient, err := redisstore.Open(startupCtx, cfg.Redis)
	if err != nil {
		logger.Error("failed to connect to redis", "error", err)
		os.Exit(1)
	}
	defer func() { _ = redisClient.Close() }()

	bus, err := natsbus.Open(cfg.NATS)
	if err != nil {
		logger.Error("failed to connect to nats", "error", err)
		os.Exit(1)
	}
	defer bus.Close()

	publisher := syncjob.NewPublisher(bus.JetStream)
	if err := publisher.EnsureStream(startupCtx); err != nil {
		logger.Error("failed to initialize sync stream", "error", err)
		os.Exit(1)
	}

	store, err := objectstore.Open(startupCtx, cfg.MinIO)
	if err != nil {
		logger.Error("failed to initialize object store", "error", err)
		os.Exit(1)
	}

	cancelStartup()

	checker := health.New(cfg)
	mediaRepository := media.NewRepository(pool)
	deps := httpserver.Dependencies{
		Access:   access.NewRepository(pool),
		Accounts: accounts.NewRepository(pool),
		Audit:    audit.NewWriter(pool),
		Chats:    chats.NewRepository(pool),
		Messages: messages.NewRepository(pool),
		Media:    media.NewService(mediaRepository, store),
		Limiter:  ratelimit.New(redisClient),
	}
	handler := httpserver.New(logger, checker, deps)

	server := &http.Server{
		Addr:              cfg.App.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("api server started", "addr", cfg.App.HTTPAddr, "env", cfg.App.Environment)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		logger.Error("http server failed", "error", err)
		os.Exit(1)
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.App.ShutdownTimeout)
	defer cancelShutdown()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}

	logger.Info("api server stopped")
}
