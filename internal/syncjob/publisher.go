package syncjob

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const StreamName = "TELEGRAM_SYNC"

type Publisher struct {
	js jetstream.JetStream
}

func NewPublisher(js jetstream.JetStream) *Publisher {
	return &Publisher{js: js}
}

func (p *Publisher) EnsureStream(ctx context.Context) error {
	_, err := p.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        StreamName,
		Description: "Telegram account synchronization and media download work queue",
		Subjects: []string{
			"telegram.sync.>",
			"telegram.media.>",
		},
		Retention:  jetstream.WorkQueuePolicy,
		Storage:    jetstream.FileStorage,
		Discard:    jetstream.DiscardOld,
		MaxAge:     7 * 24 * time.Hour,
		MaxMsgs:    1_000_000,
		MaxBytes:   8 << 30,
		MaxMsgSize: 1 << 20,
		Replicas:   1,
		Duplicates: 10 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("ensure Telegram sync stream: %w", err)
	}
	return nil
}

func (p *Publisher) Publish(ctx context.Context, envelope Envelope) error {
	if err := envelope.Validate(); err != nil {
		return err
	}
	subject, err := SubjectFor(envelope.Kind)
	if err != nil {
		return err
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal sync job envelope: %w", err)
	}

	msgID := string(envelope.Kind) + ":" + envelope.DedupKey
	if _, err := p.js.Publish(ctx, subject, body, jetstream.WithMsgID(msgID)); err != nil {
		return fmt.Errorf("publish %s sync job: %w", envelope.Kind, err)
	}
	return nil
}

func (p *Publisher) EnqueueAccountBootstrap(ctx context.Context, accountID string, force bool) error {
	dedupKey := strings.TrimSpace(accountID)
	if force {
		dedupKey += ":force:" + time.Now().UTC().Format("200601021504")
	}
	envelope, err := NewEnvelope(KindAccountBootstrap, accountID, dedupKey, AccountBootstrapPayload{Force: force})
	if err != nil {
		return err
	}
	return p.Publish(ctx, envelope)
}

func (p *Publisher) EnqueueChatHistory(ctx context.Context, accountID string, payload ChatHistoryPayload) error {
	if strings.TrimSpace(payload.ChatID) == "" {
		return fmt.Errorf("chat id is required")
	}
	if payload.TelegramChatID == 0 {
		return fmt.Errorf("telegram chat id is required")
	}
	if payload.RequestedPageSize <= 0 {
		payload.RequestedPageSize = 50
	}
	if payload.RequestedPageSize > 100 {
		payload.RequestedPageSize = 100
	}
	dedupKey := fmt.Sprintf("%s:%d:%d", payload.ChatID, payload.BeforeMessageID, payload.RequestedPageSize)
	envelope, err := NewEnvelope(KindChatHistory, accountID, dedupKey, payload)
	if err != nil {
		return err
	}
	return p.Publish(ctx, envelope)
}

func (p *Publisher) EnqueueMediaDownload(ctx context.Context, accountID string, payload MediaDownloadPayload) error {
	if strings.TrimSpace(payload.MediaID) == "" || strings.TrimSpace(payload.MessageID) == "" {
		return fmt.Errorf("media id and message id are required")
	}
	if payload.TelegramFileID == 0 {
		return fmt.Errorf("telegram file id is required")
	}
	envelope, err := NewEnvelope(KindMediaDownload, accountID, payload.MediaID, payload)
	if err != nil {
		return err
	}
	return p.Publish(ctx, envelope)
}
