package syncjob

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
)

// EnqueueChatMembersPage uses the opaque adapter cursor in the dedup identity,
// allowing large chats to sync across multiple pages without JetStream
// suppressing later pages as duplicates.
func (p *Publisher) EnqueueChatMembersPage(ctx context.Context, accountID string, payload ChatMembersPayload) error {
	if strings.TrimSpace(payload.ChatID) == "" || payload.TelegramChatID == 0 {
		return fmt.Errorf("chat id and Telegram chat id are required")
	}
	if payload.Limit <= 0 {
		payload.Limit = 100
	}
	if payload.Limit > 200 {
		payload.Limit = 200
	}

	cursorDigest := sha256.Sum256([]byte(payload.Cursor))
	dedupKey := fmt.Sprintf("%s:%x:%d", payload.ChatID, cursorDigest[:8], payload.Limit)
	envelope, err := NewEnvelope(KindChatMembers, accountID, dedupKey, payload)
	if err != nil {
		return err
	}
	return p.Publish(ctx, envelope)
}
