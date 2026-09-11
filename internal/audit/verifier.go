package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type VerificationResult struct {
	ChainKey    string
	Entries     int64
	FirstID     int64
	LastID      int64
	HeadHashHex string
}

type ChainError struct {
	ChainKey string
	EntryID  int64
	Reason   string
}

func (e *ChainError) Error() string {
	return fmt.Sprintf("audit chain %q failed at entry %d: %s", e.ChainKey, e.EntryID, e.Reason)
}

type Verifier struct {
	pool *pgxpool.Pool
}

func NewVerifier(pool *pgxpool.Pool) *Verifier {
	return &Verifier{pool: pool}
}

func (v *Verifier) VerifyAll(ctx context.Context) ([]VerificationResult, error) {
	rows, err := v.pool.Query(ctx, `SELECT DISTINCT chain_key FROM audit_logs ORDER BY chain_key`)
	if err != nil {
		return nil, fmt.Errorf("list audit chains: %w", err)
	}
	defer rows.Close()

	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan audit chain key: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit chain keys: %w", err)
	}

	results := make([]VerificationResult, 0, len(keys))
	for _, key := range keys {
		result, err := v.VerifyChain(ctx, key)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func (v *Verifier) VerifyChain(ctx context.Context, chainKey string) (VerificationResult, error) {
	chainKey = strings.TrimSpace(chainKey)
	if chainKey == "" {
		return VerificationResult{}, errors.New("audit chain key is required")
	}

	rows, err := v.pool.Query(ctx, `
		SELECT id, account_id::text, actor_type, actor_id, action, resource_type,
		       resource_id, host(ip_address), user_agent, request_id::text, metadata,
		       previous_hash, hash, created_at
		FROM audit_logs
		WHERE chain_key = $1
		ORDER BY id ASC`, chainKey)
	if err != nil {
		return VerificationResult{}, fmt.Errorf("read audit chain %q: %w", chainKey, err)
	}
	defer rows.Close()

	result := VerificationResult{ChainKey: chainKey}
	var previousHash []byte
	for rows.Next() {
		var (
			id           int64
			accountID    *string
			actorType    string
			actorID      *string
			action       string
			resourceType string
			resourceID   *string
			ipAddress    *string
			userAgent    *string
			requestID    *string
			metadataJSON []byte
			storedPrev   []byte
			storedHash   []byte
			createdAt    time.Time
		)

		if err := rows.Scan(
			&id,
			&accountID,
			&actorType,
			&actorID,
			&action,
			&resourceType,
			&resourceID,
			&ipAddress,
			&userAgent,
			&requestID,
			&metadataJSON,
			&storedPrev,
			&storedHash,
			&createdAt,
		); err != nil {
			return VerificationResult{}, fmt.Errorf("scan audit entry: %w", err)
		}

		if !bytes.Equal(storedPrev, previousHash) {
			return VerificationResult{}, &ChainError{ChainKey: chainKey, EntryID: id, Reason: "previous hash mismatch"}
		}

		canonicalMetadata, err := normalizeJSON(metadataJSON)
		if err != nil {
			return VerificationResult{}, &ChainError{ChainKey: chainKey, EntryID: id, Reason: "invalid metadata: " + err.Error()}
		}
		event := Event{
			AccountID:    accountID,
			ActorType:    ActorType(actorType),
			ActorID:      valueOrEmpty(actorID),
			Action:       action,
			ResourceType: resourceType,
			ResourceID:   valueOrEmpty(resourceID),
			IPAddress:    valueOrEmpty(ipAddress),
			UserAgent:    valueOrEmpty(userAgent),
			RequestID:    requestID,
			CreatedAt:    createdAt.UTC(),
		}
		payload, err := canonicalPayload(event, canonicalMetadata)
		if err != nil {
			return VerificationResult{}, &ChainError{ChainKey: chainKey, EntryID: id, Reason: err.Error()}
		}

		hasher := sha256.New()
		_, _ = hasher.Write(previousHash)
		_, _ = hasher.Write(payload)
		expectedHash := hasher.Sum(nil)
		if !bytes.Equal(storedHash, expectedHash) {
			return VerificationResult{}, &ChainError{ChainKey: chainKey, EntryID: id, Reason: "entry hash mismatch"}
		}

		if result.Entries == 0 {
			result.FirstID = id
		}
		result.Entries++
		result.LastID = id
		previousHash = append(previousHash[:0], storedHash...)
	}
	if err := rows.Err(); err != nil {
		return VerificationResult{}, fmt.Errorf("iterate audit chain %q: %w", chainKey, err)
	}

	result.HeadHashHex = fmt.Sprintf("%x", previousHash)
	return result, nil
}

func normalizeJSON(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return []byte("{}"), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
