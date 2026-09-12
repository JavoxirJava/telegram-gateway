// tdlib-agent is an operator-started, one-account native session host. It binds
// authentication only to an owner-only Unix socket, never to a TCP listener.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/accounts"
	"github.com/JavoxirJava/telegram-gateway/internal/accountsync"
	"github.com/JavoxirJava/telegram-gateway/internal/config"
	"github.com/JavoxirJava/telegram-gateway/internal/livepg"
	"github.com/JavoxirJava/telegram-gateway/internal/natsbus"
	"github.com/JavoxirJava/telegram-gateway/internal/objectstore"
	"github.com/JavoxirJava/telegram-gateway/internal/postgres"
	"github.com/JavoxirJava/telegram-gateway/internal/ratelimit"
	"github.com/JavoxirJava/telegram-gateway/internal/redisstore"
	sessionruntime "github.com/JavoxirJava/telegram-gateway/internal/runtime"
	"github.com/JavoxirJava/telegram-gateway/internal/sessionkey"
	"github.com/JavoxirJava/telegram-gateway/internal/tdadapter"
	"github.com/JavoxirJava/telegram-gateway/internal/tdjson"
	"github.com/JavoxirJava/telegram-gateway/internal/tdlib"
	"github.com/JavoxirJava/telegram-gateway/internal/tglive"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	account := flag.String("account", "", "existing gateway account UUID")
	root := flag.String("sessions-root", "/var/lib/telegram-gateway/sessions", "owner-only persistent session directory")
	library := flag.String("library", "/usr/local/lib/libtdjson.so", "absolute trusted TDLib shared-library path")
	master := flag.String("master-key-file", "", "0600 file containing a base64 32-byte master key")
	consent := flag.Bool("enable-live-mirror", false, "explicitly enable authorized live and historical cloud-message synchronization")
	flag.Parse()
	if !*consent {
		logger.Error("live mirror requires explicit --enable-live-mirror")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger, *account, *root, *library, *master); err != nil {
		logger.Error("TDLib agent stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger, accountID, root, library, masterFile string) (returnErr error) {
	if !sessionkey.ValidAccountID(accountID) {
		return errors.New("--account requires a canonical lowercase UUID")
	}
	cfg, err := config.Load()
	if err != nil {
		return errors.New("invalid agent environment configuration")
	}
	master, err := sessionkey.LoadMasterFile(masterFile)
	if err != nil {
		return err
	}
	defer clear(master)
	keyring, err := sessionkey.New("primary", map[string][]byte{"primary": master})
	if err != nil {
		return err
	}
	workspace, err := sessionkey.OpenWorkspace(root, accountID, keyring)
	if err != nil {
		return err
	}
	cleanNative := true
	defer func() {
		if cleanNative {
			_ = workspace.Close()
		}
	}()
	startup, cancel := context.WithTimeout(ctx, 20*time.Second)
	pool, err := postgres.Open(startup, cfg.Postgres)
	cancel()
	if err != nil {
		return errors.New("cannot connect to agent database")
	}
	defer pool.Close()
	redisCtx, cancelRedis := context.WithTimeout(ctx, 10*time.Second)
	redisClient, err := redisstore.Open(redisCtx, cfg.Redis)
	cancelRedis()
	if err != nil {
		return errors.New("cannot connect to native request governor")
	}
	defer redisClient.Close()
	requests, err := tdlib.NewRedisPolicy(ratelimit.New(redisClient), accountID)
	if err != nil {
		return err
	}
	accountRepo := accounts.NewRepository(pool)
	account, err := accountRepo.Get(ctx, accountID)
	if err != nil {
		return errors.New("gateway account does not exist")
	}
	if account.Status != accounts.StatusPending && account.Status != accounts.StatusActive {
		return errors.New("account is not available for session ownership")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	workerID := "tdlib-" + hex.EncodeToString(nonce[:])
	runtimeRepo := sessionruntime.NewRepository(pool)
	lease, owned, err := runtimeRepo.Acquire(ctx, accountID, workerID, "local", 45*time.Second)
	if err != nil {
		return errors.New("cannot acquire session lease")
	}
	if !owned {
		return errors.New("Telegram account is owned by another agent or is offline")
	}
	defer func() {
		if cleanNative {
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = runtimeRepo.Release(c, lease)
		}
	}()

	lifeCtx, stopLife := context.WithCancel(ctx)
	defer stopLife()
	var lost atomic.Bool
	failures := make(chan error, 4)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-lifeCtx.Done():
				return
			case <-ticker.C:
				c, cancel := context.WithTimeout(lifeCtx, 5*time.Second)
				_, err := runtimeRepo.Renew(c, lease, 45*time.Second)
				cancel()
				if err != nil {
					lost.Store(true)
					failures <- errors.New("session lease lost; stopping native agent")
					return
				}
			}
		}
	}()
	transport, err := tdjson.OpenNative(library)
	if err != nil {
		return err
	}
	engine, err := tdjson.New(transport)
	if err != nil {
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := engine.Close(c); err != nil {
			cleanNative = false
			returnErr = errors.Join(returnErr, errors.New("native shutdown incomplete; process exit required before session reuse"))
		}
	}()
	index := tdadapter.NewIndex()
	connectionReady := make(chan struct{}, 1)
	var connected atomic.Bool
	queue := accountsync.NewQueue(pool, lease)
	var notifier atomic.Pointer[nats.Conn]
	normalizer := tglive.New()
	store := livepg.NewWithLease(pool, lease)
	store.After = func(c context.Context, tx pgx.Tx, chatID string, e tglive.Event) error {
		switch e.Kind {
		case "message", "content", "edited":
			return queue.EnqueueTx(c, tx, "refresh", accountsync.Payload{ChatID: chatID, TelegramChatID: e.ChatID, MessageID: e.MessageID}, true)
		case "allowed":
			return queue.EnqueueTx(c, tx, "history", accountsync.Payload{ChatID: chatID, TelegramChatID: e.ChatID}, true)
		}
		return nil
	}
	gate := tglive.NewGate(func(c context.Context, raw json.RawMessage) error {
		if lost.Load() {
			return errors.New("session lease is no longer owned")
		}
		event, relevant, err := normalizer.Decode(raw)
		if err != nil || !relevant {
			return err
		}
		var eventNonce [16]byte
		if _, err = rand.Read(eventNonce[:]); err != nil {
			return err
		}
		encoded := hex.EncodeToString(eventNonce[:])
		eventKey := encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
		// A database outage backpressures the ordered stream instead of silently
		// losing updates. Raw content is never written to operational logs.
		for {
			if lost.Load() {
				return errors.New("session lease is no longer owned")
			}
			attempt, done := context.WithTimeout(c, 5*time.Second)
			err = store.Accept(attempt, accountID, eventKey, event)
			if err == nil {
				err = store.Replay(attempt, accountID)
			}
			done()
			if err == nil {
				if nc := notifier.Load(); nc != nil {
					_ = nc.Publish("gateway.wake."+accountID, []byte("1"))
				}
				return nil
			}
			if errors.Is(err, livepg.ErrLeaseLost) || errors.Is(err, sessionruntime.ErrLeaseLost) {
				return err
			}
			select {
			case <-c.Done():
				return c.Err()
			case <-time.After(time.Second):
			}
		}
	})
	session, err := tdlib.New(engine, tdlib.Config{APIID: int(cfg.Telegram.APIID), APIHash: cfg.Telegram.APIHash, DatabaseDirectory: workspace.DatabaseDir, FilesDirectory: workspace.FilesDir, DatabaseKey: workspace.DatabaseKey, Requests: requests}, func(c context.Context, raw json.RawMessage) error {
		if err := index.Observe(c, raw); err != nil {
			return err
		}
		var state struct {
			Type       string `json:"@type"`
			Connection struct {
				Type string `json:"@type"`
			} `json:"state"`
		}
		if json.Unmarshal(raw, &state) != nil {
			return errors.New("invalid connection event")
		}
		if state.Type == "updateConnectionState" {
			ready := state.Connection.Type == "connectionStateReady"
			old := connected.Swap(ready)
			if ready && !old {
				select {
				case connectionReady <- struct{}{}:
				default:
				}
			}
		}
		return gate.Handle(c, raw)
	})
	if err != nil {
		return err
	}
	if err := runtimeRepo.SetObservedState(ctx, lease, sessionruntime.StateAuthorizing, ""); err != nil {
		return err
	}
	socketPath := filepath.Join(filepath.Dir(workspace.DatabaseDir), "control.sock")
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("control socket path is occupied by a non-socket file")
		}
		if err := os.Remove(socketPath); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return errors.New("cannot create private control socket")
	}
	defer listener.Close()
	defer os.Remove(socketPath)
	if err := os.Chmod(socketPath, 0600); err != nil {
		return err
	}
	server := &http.Server{Handler: tdlib.ControlHandler(session), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	defer server.Close()
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failures <- errors.New("private control server failed")
		}
	}()
	logger.Info("TDLib private control socket ready", "account_id", accountID, "socket", socketPath)
	initialize, cancelInitialize := context.WithTimeout(ctx, 60*time.Second)
	err = session.Initialize(initialize)
	cancelInitialize()
	if err != nil {
		return err
	}
	ready := make(chan struct{})
	activationDone := make(chan struct{})
	defer func() { stopLife(); <-activationDone }()
	go func() {
		defer close(activationDone)
		if err := session.WaitReady(lifeCtx); err != nil {
			failures <- errors.New("native session closed before authorization")
			return
		}
		raw, err := session.Read(lifeCtx, "getMe", nil)
		if err != nil {
			failures <- errors.New("cannot verify Telegram account identity")
			return
		}
		var me struct {
			ID        int64  `json:"id"`
			FirstName string `json:"first_name"`
			LastName  string `json:"last_name"`
			Usernames struct {
				Active []string `json:"active_usernames"`
			} `json:"usernames"`
		}
		if json.Unmarshal(raw, &me) != nil || me.ID <= 0 {
			failures <- errors.New("invalid Telegram identity response")
			return
		}
		if account.TelegramUserID != nil && *account.TelegramUserID != me.ID {
			failures <- errors.New("Telegram identity does not match the registered account")
			return
		}
		username := ""
		if len(me.Usernames.Active) > 0 {
			username = me.Usernames.Active[0]
		}
		if lost.Load() {
			return
		}
		if err := runtimeRepo.ActivateVerified(lifeCtx, lease, me.ID, me.FirstName+" "+me.LastName, username); err != nil {
			failures <- errors.New("cannot activate verified Telegram account")
			return
		}
		if err := store.Replay(lifeCtx, accountID); err != nil {
			failures <- errors.New("cannot replay persisted live updates")
			return
		}
		if err := gate.Open(lifeCtx); err != nil {
			failures <- errors.New("cannot commit initial live updates")
			return
		}
		if err := runtimeRepo.SetObservedState(lifeCtx, lease, sessionruntime.StateReady, ""); err != nil {
			failures <- errors.New("session lease lost during activation")
			return
		}
		logger.Info("verified Telegram live mirror ready", "account_id", accountID)
		close(ready)
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		return nil
	case <-session.Done():
		return errors.New("native session stopped")
	case err := <-failures:
		return err
	}
	adapter, err := tdadapter.New(session, index, tdadapter.Config{AccountID: accountID, FilesDirectory: workspace.FilesDir, MaxFileBytes: 1 << 30, Authorize: func(c context.Context) error {
		if lost.Load() {
			return sessionruntime.ErrLeaseLost
		}
		if session.State().Type != tdlib.Ready || !connected.Load() {
			return &tdlib.RequestThrottled{After: 15 * time.Second}
		}
		return runtimeRepo.Check(c, lease)
	}})
	if err != nil {
		return err
	}
	defer adapter.Close()
	bus, err := natsbus.Open(cfg.NATS)
	if err != nil {
		return errors.New("cannot open account wakeup bus")
	}
	defer bus.Close()
	notifier.Store(bus.Conn)
	defer notifier.Store(nil)
	storageCtx, cancelStorage := context.WithTimeout(lifeCtx, 15*time.Second)
	objects, err := objectstore.Open(storageCtx, cfg.MinIO)
	cancelStorage()
	if err != nil {
		return errors.New("cannot open media storage")
	}
	runner, err := accountsync.NewRunner(queue, adapter, objects, logger)
	if err != nil {
		return err
	}
	workerCtx, stopWorker := context.WithCancel(lifeCtx)
	workerDone := make(chan error, 1)
	go func() { workerDone <- runner.Run(workerCtx, bus.Conn, bus.JetStream, connectionReady) }()
	defer func() { stopWorker(); <-workerDone }()
	logger.Info("account-routed history and media worker ready", "account_id", accountID)
	select {
	case <-ctx.Done():
		return nil
	case <-session.Done():
		return errors.New("native session stopped")
	case err := <-failures:
		return err
	case err := <-workerDone:
		workerDone <- err
		return err
	}
}
