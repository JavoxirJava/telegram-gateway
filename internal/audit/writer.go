package audit

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ActorType string

const (
	ActorUser     ActorType = "USER"
	ActorMCP      ActorType = "MCP"
	ActorSystem   ActorType = "SYSTEM"
	ActorTelegram ActorType = "TELEGRAM"
	ActorAdmin    ActorType = "ADMIN"
)

const auditChainLockID int64 = 0x54474155444954

type Event struct {
	AccountID    *string
	ActorType    ActorType
	ActorID      string
	Action       string
	ResourceType string
	ResourceID   string
	IPAddress    string
	UserAgent    string
	RequestID    *string
	Metadata     map[string]any
	CreatedAt    time.Time
}

type Writer struct {
	pool *pgxpool.Pool
}

func NewWriter(pool *pgxpool.Pool) *Writer {
	return &Writer{pool: pool}
}

func (w *Writer) Write(ctx context.Context, event Event) error {
	if err := event.validate(); err != nil {
		return err
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	} else {
		event.CreatedAt = event.CreatedAt.UTC()
	}
	if event.Metadata == nil {
		event.Metadata = map[string]any{}
	}

	metadataJSON, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("marshal audit metadata: %w", err)
	}

	tx, err := w.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin audit transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", auditChainLockID); err != nil {
		return fmt.Errorf("lock audit chain: %w", err)
	}

	var previousHash []byte
	err = tx.QueryRow(ctx, "SELECT hash FROM audit_logs ORDER BY id DESC LIMIT 1").Scan(&previousHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read previous audit hash: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		previousHash = nil
	}

	payload, err := canonicalPayload(event, metadataJSON)
	if err != nil {
		return err
	}

	hasher := sha256.New()
	_, _ = hasher.Write(previousHash)
	_, _ = hasher.Write(payload)
	currentHash := hasher.Sum(nil)

	_, err = tx.Exec(ctx, `
		INSERT INTO audit_logs (
			account_id, actor_type, actor_id, action, resource_type, resource_id,
			ip_address, user_agent, request_id, metadata, previous_hash, hash, created_at
		) VALUES (
			$1::uuid, $2, NULLIF($3, ''), $4, $5, NULLIF($6, ''),
			NULLIF($7, '')::inet, NULLIF($8, ''), $9::uuid, $10::jsonb, $11, $12, $13
		)`,
		event.AccountID,
		string(event.ActorType),
		event.ActorID,
		event.Action,
		event.ResourceType,
		event.ResourceID,
		event.IPAddress,
		event.UserAgent,
		event.RequestID,
		string(metadataJSON),
		previousHash,
		currentHash,
		event.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit transaction: %w", err)
	}
	return nil
}

func canonicalPayload(event Event, metadataJSON []byte) ([]byte, error) {
	payload := struct {
		AccountID    *string         `json:"account_id"`
		ActorType    ActorType       `json:"actor_type"`
		ActorID      string          `json:"actor_id"`
		Action       string          `json:"action"`
		ResourceType string          `json:"resource_type"`
		ResourceID   string          `json:"resource_id"`
		IPAddress    string          `json:"ip_address"`
		UserAgent    string          `json:"user_agent"`
		RequestID    *string         `json:"request_id"`
		Metadata     json.RawMessage `json:"metadata"`
		CreatedAt    string          `json:"created_at"`
	}{
		AccountID:    event.AccountID,
		ActorType:    event.ActorType,
		ActorID:      event.ActorID,
		Action:       event.Action,
		ResourceType: event.ResourceType,
		ResourceID:   event.ResourceID,
		IPAddress:    event.IPAddress,
		UserAgent:    event.UserAgent,
		RequestID:    event.RequestID,
		Metadata:     metadataJSON,
		CreatedAt:    event.CreatedAt.Format(time.RFC3339Nano),
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal audit payload: %w", err)
	}
	return encoded, nil
}

func (event Event) validate() error {
	switch event.ActorType {
	case ActorUser, ActorMCP, ActorSystem, ActorTelegram, ActorAdmin:
	default:
		return fmt.Errorf("invalid audit actor type %q", event.ActorType)
	}
	if strings.TrimSpace(event.Action) == "" {
		return errors.New("audit action is required")
	}
	if strings.TrimSpace(event.ResourceType) == "" {
		return errors.New("audit resource type is required")
	}
	return nil
}
