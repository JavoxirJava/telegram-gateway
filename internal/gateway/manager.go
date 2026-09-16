package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/accounts"
	"github.com/JavoxirJava/telegram-gateway/internal/config"
	sessionruntime "github.com/JavoxirJava/telegram-gateway/internal/runtime"
	"github.com/JavoxirJava/telegram-gateway/internal/syncjob"
	"github.com/JavoxirJava/telegram-gateway/internal/tdlib"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Manager struct {
	pool      *pgxpool.Pool
	config    config.TelegramConfig
	key       []byte
	owner     string
	workerID  string
	sessions  map[string]*tdlib.Session
	ready     map[string]bool
	mu        sync.RWMutex
	loginMu   sync.Mutex
	publisher *syncjob.Publisher
	logger    *slog.Logger
}

func New(pool *pgxpool.Pool, cfg config.TelegramConfig, key []byte, publisher *syncjob.Publisher, logger *slog.Logger) *Manager {
	return &Manager{pool: pool, config: cfg, key: key, workerID: uuid.NewString(), sessions: map[string]*tdlib.Session{}, ready: map[string]bool{}, publisher: publisher, logger: logger}
}
func (m *Manager) WorkerID() string { return m.workerID }
func (m *Manager) Configured() bool {
	return m.config.APIID > 0 && m.config.APIHash != "" && len(m.key) == 32
}
func (m *Manager) Initialize(ctx context.Context) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(736487232)`); err != nil {
		return err
	}
	var owner string
	if err := tx.QueryRow(ctx, `SELECT value FROM gateway_settings WHERE key='owner_user_id'`).Scan(&owner); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO app_users DEFAULT VALUES RETURNING id::text`).Scan(&owner); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO gateway_settings(key,value) VALUES('owner_user_id',$1)`, owner); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	m.owner = owner
	return nil
}
func (m *Manager) OwnerID() string { return m.owner }
func (m *Manager) CreateAccount(ctx context.Context) (accounts.Account, error) {
	return accounts.NewRepository(m.pool).CreatePending(ctx, m.owner)
}
func (m *Manager) Get(ctx context.Context, id string) (telegram.Session, error) {
	m.mu.RLock()
	s := m.sessions[id]
	ready := m.ready[id]
	m.mu.RUnlock()
	if s == nil || !ready || !s.IsReady() {
		return nil, errors.New("Telegram account is not ready")
	}
	return s, nil
}
func (m *Manager) AccountState(id string) map[string]any {
	m.mu.RLock()
	s := m.sessions[id]
	m.mu.RUnlock()
	if s == nil {
		return map[string]any{"state": "offline", "configured": m.Configured()}
	}
	return s.State()
}
func (m *Manager) Authorize(ctx context.Context, id, action, value string) error {
	m.mu.RLock()
	s := m.sessions[id]
	m.mu.RUnlock()
	if s == nil {
		return errors.New("Telegram session is starting or credentials are missing")
	}
	return s.Authorize(ctx, action, value)
}
func (m *Manager) Run(ctx context.Context) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		if m.Configured() {
			rows, err := m.pool.Query(ctx, `SELECT ta.id::text FROM telegram_accounts ta LEFT JOIN telegram_session_runtime r ON r.account_id=ta.id WHERE ta.status IN ('pending','active') AND COALESCE(r.desired_state,'online')='online'`)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				m.logger.Error("list Telegram accounts", "error", err)
			} else {
				var ids []string
				for rows.Next() {
					var id string
					if rows.Scan(&id) == nil {
						ids = append(ids, id)
					}
				}
				rows.Close()
				for _, id := range ids {
					m.mu.Lock()
					_, running := m.sessions[id]
					if !running {
						m.sessions[id] = nil
					}
					m.mu.Unlock()
					if !running {
						wg.Add(1)
						go func(id string) { defer wg.Done(); m.runAccount(ctx, id) }(id)
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (m *Manager) runAccount(ctx context.Context, id string) {
	defer func() { m.mu.Lock(); delete(m.sessions, id); delete(m.ready, id); m.mu.Unlock() }()
	repo := sessionruntime.NewRepository(m.pool)
	lease, ok, err := repo.Acquire(ctx, id, m.workerID, "pc", 45*time.Second)
	if err != nil || !ok {
		if err != nil {
			m.logger.Error("acquire Telegram session", "account_id", id, "error", err)
		}
		return
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = repo.Release(c, lease)
	}()
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	var storageID string
	var expectedUser *int64
	if err := m.pool.QueryRow(run, `SELECT session_directory_id::text,telegram_user_id FROM telegram_accounts WHERE id=$1::uuid`, id).Scan(&storageID, &expectedUser); err != nil {
		return
	}
	var eventMu sync.Mutex
	var buffered []tdlib.Event
	verified := false
	persist := func(event tdlib.Event) error {
		body, err := json.Marshal(event)
		if err != nil {
			return err
		}
		c, stop := context.WithTimeout(run, 10*time.Second)
		defer stop()
		tag, err := m.pool.Exec(c, `INSERT INTO telegram_updates(account_id,payload) SELECT $1::uuid,$2::jsonb FROM telegram_session_runtime WHERE account_id=$1::uuid AND worker_id=$3 AND lease_token=$4::uuid AND generation=$5 AND lease_expires_at>NOW()`, id, string(body), lease.WorkerID, lease.Token, lease.Generation)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("session ownership lost")
		}
		return nil
	}
	s, err := tdlib.Open(run, tdlib.Options{AccountID: storageID, APIID: m.config.APIID, APIHash: m.config.APIHash, MasterKey: m.key, Directory: m.config.DataDir, OnUpdate: func(event tdlib.Event) error {
		eventMu.Lock()
		defer eventMu.Unlock()
		if !verified {
			if len(buffered) >= 10000 {
				return errors.New("Telegram identity verification backlog exceeded")
			}
			buffered = append(buffered, event)
			return nil
		}
		return persist(event)
	}})
	if err != nil {
		_ = repo.SetObservedState(ctx, lease, sessionruntime.StateError, err.Error())
		m.logger.Error("start Telegram session", "account_id", id, "error", err)
		return
	}
	defer s.Close()
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	lastRenew := time.Now()
	activated := false
	lastSync := time.Time{}
	for {
		select {
		case <-run.Done():
			return
		case <-s.Done():
			return
		case <-tick.C:
		}
		if expectedUser != nil && !activated {
			state := s.State()["state"]
			if state == "authorizationStateWaitPhoneNumber" || state == "authorizationStateLoggingOut" {
				_ = accounts.NewRepository(m.pool).MarkDisconnected(run, id)
				return
			}
		}
		if activated && !s.IsReady() {
			_ = accounts.NewRepository(m.pool).MarkDisconnected(run, id)
			return
		}
		if time.Since(lastRenew) >= 10*time.Second {
			if _, err := repo.Renew(run, lease, 45*time.Second); err != nil {
				return
			}
			lastRenew = time.Now()
			var desired string
			if err := m.pool.QueryRow(run, `SELECT desired_state FROM telegram_session_runtime WHERE account_id=$1::uuid`, id).Scan(&desired); err != nil || desired != "online" {
				return
			}
		}
		if activated && time.Since(lastSync) > 30*time.Minute {
			if err := m.publisher.EnqueueAccountBootstrap(run, id, true); err == nil {
				lastSync = time.Now()
			}
		}
		if !activated && s.IsReady() {
			p, err := s.Profile(run)
			if err != nil {
				continue
			}
			if expectedUser != nil && *expectedUser != p.TelegramUserID {
				_ = accounts.NewRepository(m.pool).MarkDisconnected(run, id)
				m.logger.Error("Telegram session identity mismatch", "account_id", id)
				return
			}
			eventMu.Lock()
			for _, event := range buffered {
				if err = persist(event); err != nil {
					break
				}
			}
			if err == nil {
				verified = true
				buffered = nil
			}
			eventMu.Unlock()
			if err != nil {
				return
			}
			if _, err := accounts.NewRepository(m.pool).Activate(run, id, p.TelegramUserID, p.DisplayName, p.Username); err != nil {
				m.logger.Error("activate Telegram account", "error", err)
				return
			}
			if err := repo.SetObservedState(run, lease, sessionruntime.StateReady, ""); err != nil {
				return
			}
			m.mu.Lock()
			m.ready[id] = true
			m.mu.Unlock()
			if err := m.publisher.EnqueueAccountBootstrap(run, id, true); err != nil {
				m.logger.Error("enqueue Telegram bootstrap", "error", err)
				return
			}
			activated = true
			lastSync = time.Now()
		}
	}
}
func (m *Manager) AccountList(ctx context.Context) ([]map[string]any, error) {
	rows, err := m.pool.Query(ctx, `SELECT id::text,COALESCE(display_name,''),status FROM telegram_accounts WHERE user_id=$1::uuid ORDER BY created_at`, m.owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, status string
		if err := rows.Scan(&id, &name, &status); err != nil {
			return nil, err
		}
		var chatCount, messageCount, mediaCount, updateCount int64
		_ = m.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM active_chats WHERE account_id=$1::uuid),(SELECT count(*) FROM active_messages WHERE account_id=$1::uuid),(SELECT count(*) FROM active_message_media WHERE account_id=$1::uuid AND download_status='ready'),(SELECT count(*) FROM telegram_updates WHERE account_id=$1::uuid)`, id).Scan(&chatCount, &messageCount, &mediaCount, &updateCount)
		out = append(out, map[string]any{"id": id, "name": name, "status": status, "authorization": m.AccountState(id), "stats": map[string]any{"chats": chatCount, "messages": messageCount, "media": mediaCount, "pending_updates": updateCount}})
	}
	return out, rows.Err()
}
func (m *Manager) Sync(ctx context.Context, id string) error {
	if _, err := m.Get(ctx, id); err != nil {
		return err
	}
	return m.publisher.EnqueueAccountBootstrap(ctx, id, true)
}
func (m *Manager) CheckOwner(ctx context.Context, id string) error {
	var ok bool
	err := m.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM telegram_accounts WHERE id=$1::uuid AND user_id=$2::uuid)`, id, m.owner).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("account not found")
	}
	return nil
}
