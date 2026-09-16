package main

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/JavoxirJava/telegram-gateway/internal/gateway"
	"github.com/JavoxirJava/telegram-gateway/internal/syncstate"
	"github.com/JavoxirJava/telegram-gateway/internal/worker"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/accounts"
	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	"github.com/JavoxirJava/telegram-gateway/internal/chats"
	"github.com/JavoxirJava/telegram-gateway/internal/config"
	"github.com/JavoxirJava/telegram-gateway/internal/contacts"
	"github.com/JavoxirJava/telegram-gateway/internal/health"
	"github.com/JavoxirJava/telegram-gateway/internal/httpserver"
	"github.com/JavoxirJava/telegram-gateway/internal/media"
	"github.com/JavoxirJava/telegram-gateway/internal/members"
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

	checker := health.NewProbes(map[string]func(context.Context) error{
		"postgres": pool.Ping,
		"redis":    func(ctx context.Context) error { return redisClient.Ping(ctx).Err() },
		"nats":     func(ctx context.Context) error { _, err := bus.JetStream.Stream(ctx, syncjob.StreamName); return err },
		"minio":    store.Check,
	})
	mediaRepository := media.NewRepository(pool)
	key, err := base64.StdEncoding.DecodeString(cfg.Telegram.SessionKey)
	if err != nil || len(key) != 32 || len(cfg.App.AdminToken) < 32 {
		logger.Error("TELEGRAM_SESSION_KEY must encode 32 bytes and GATEWAY_ADMIN_TOKEN must contain at least 32 characters")
		os.Exit(1)
	}
	manager := gateway.New(pool, cfg.Telegram, key, publisher, logger)
	initCtx, cancelInit := context.WithTimeout(ctx, 10*time.Second)
	if err := manager.Initialize(initCtx); err != nil {
		logger.Error("initialize gateway owner", "error", err)
		cancelInit()
		os.Exit(1)
	}
	cancelInit()
	deps := httpserver.Dependencies{
		Pool: pool, Manager: manager, BaseURL: cfg.App.PublicURL, AdminToken: cfg.App.AdminToken,
		Access:   access.NewRepository(pool),
		Accounts: accounts.NewRepository(pool),
		Audit:    audit.NewWriter(pool),
		Chats:    chats.NewRepository(pool),
		Contacts: contacts.NewRepository(pool),
		Members:  members.NewRepository(pool),
		Messages: messages.NewRepository(pool),
		Media:    media.NewService(mediaRepository, store),
		Limiter:  ratelimit.New(redisClient),
	}
	processor, err := worker.NewProcessor(worker.Dependencies{WorkerID: manager.WorkerID(), Sessions: manager, Accounts: deps.Accounts, Chats: deps.Chats, Messages: deps.Messages, Contacts: deps.Contacts, Members: deps.Members, MediaRepo: mediaRepository, Media: deps.Media, SyncStates: syncstate.NewRepository(pool), Publisher: publisher, Limiter: deps.Limiter, Audit: deps.Audit})
	if err != nil {
		logger.Error("initialize sync processor", "error", err)
		os.Exit(1)
	}
	runner, err := worker.NewRunner(logger, bus.JetStream, processor, 4)
	if err != nil {
		logger.Error("initialize workers", "error", err)
		os.Exit(1)
	}
	handler := httpserver.New(logger, checker, deps)
	defer handler.Close()

	server := &http.Server{
		Addr:              cfg.App.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	errCh := make(chan error, 4)
	var workers sync.WaitGroup
	workers.Add(4)
	go func() { defer workers.Done(); gateway.Maintain(ctx, pool) }()
	go func() {
		defer workers.Done()
		if err := manager.Run(ctx); err != nil {
			errCh <- err
		}
	}()
	go func() {
		defer workers.Done()
		if err := runner.Run(ctx); err != nil {
			errCh <- err
		}
	}()
	go func() {
		defer workers.Done()
		if err := processor.RunUpdates(ctx, pool); err != nil {
			errCh <- err
		}
	}()
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
		logger.Error("gateway worker or HTTP server failed", "error", err)
		stop()
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.App.ShutdownTimeout)
	defer cancelShutdown()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}

	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		logger.Warn("worker shutdown timed out")
	}
	logger.Info("api server stopped")
}
