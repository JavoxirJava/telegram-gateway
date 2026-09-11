package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/accounts"
	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	"github.com/JavoxirJava/telegram-gateway/internal/chats"
	"github.com/JavoxirJava/telegram-gateway/internal/contacts"
	"github.com/JavoxirJava/telegram-gateway/internal/media"
	"github.com/JavoxirJava/telegram-gateway/internal/members"
	"github.com/JavoxirJava/telegram-gateway/internal/messages"
	"github.com/JavoxirJava/telegram-gateway/internal/ratelimit"
	"github.com/JavoxirJava/telegram-gateway/internal/syncjob"
	"github.com/JavoxirJava/telegram-gateway/internal/syncstate"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
)

type Publisher interface {
	EnqueueAccountBootstrapPage(ctx context.Context, accountID, cursor string) error
	EnqueueContactsSync(ctx context.Context, accountID string) error
	EnqueueChatHistory(ctx context.Context, accountID string, payload syncjob.ChatHistoryPayload) error
	EnqueueChatMembers(ctx context.Context, accountID string, payload syncjob.ChatMembersPayload) error
	EnqueueMediaDownload(ctx context.Context, accountID string, payload syncjob.MediaDownloadPayload) error
}

type Processor struct {
	workerID   string
	sessions   telegram.Sessions
	accounts   *accounts.Repository
	chats      *chats.Repository
	messages   *messages.Repository
	contacts   *contacts.Repository
	members    *members.Repository
	mediaRepo  *media.Repository
	media      *media.Service
	syncStates *syncstate.Repository
	publisher  Publisher
	limiter    *ratelimit.Limiter
	policies   ratelimit.Policies
	audit      *audit.Writer
}

type Dependencies struct {
	WorkerID   string
	Sessions   telegram.Sessions
	Accounts   *accounts.Repository
	Chats      *chats.Repository
	Messages   *messages.Repository
	Contacts   *contacts.Repository
	Members    *members.Repository
	MediaRepo  *media.Repository
	Media      *media.Service
	SyncStates *syncstate.Repository
	Publisher  Publisher
	Limiter    *ratelimit.Limiter
	Audit      *audit.Writer
}

func NewProcessor(deps Dependencies) (*Processor, error) {
	if strings.TrimSpace(deps.WorkerID) == "" {
		return nil, errors.New("worker id is required")
	}
	if deps.Sessions == nil || deps.Accounts == nil || deps.Chats == nil || deps.Messages == nil ||
		deps.Contacts == nil || deps.Members == nil || deps.MediaRepo == nil || deps.Media == nil ||
		deps.SyncStates == nil || deps.Publisher == nil || deps.Limiter == nil || deps.Audit == nil {
		return nil, errors.New("worker dependencies are incomplete")
	}
	return &Processor{
		workerID:   strings.TrimSpace(deps.WorkerID),
		sessions:   deps.Sessions,
		accounts:   deps.Accounts,
		chats:      deps.Chats,
		messages:   deps.Messages,
		contacts:   deps.Contacts,
		members:    deps.Members,
		mediaRepo:  deps.MediaRepo,
		media:      deps.Media,
		syncStates: deps.SyncStates,
		publisher:  deps.Publisher,
		limiter:    deps.Limiter,
		policies:   ratelimit.DefaultPolicies(),
		audit:      deps.Audit,
	}, nil
}

func (p *Processor) Handle(ctx context.Context, envelope syncjob.Envelope) error {
	if err := envelope.Validate(); err != nil {
		return Permanent(err)
	}

	switch envelope.Kind {
	case syncjob.KindAccountBootstrap:
		var payload syncjob.AccountBootstrapPayload
		if err := decodePayload(envelope.Payload, &payload); err != nil {
			return Permanent(err)
		}
		return p.handleAccountBootstrap(ctx, envelope, payload)
	case syncjob.KindContactsSync:
		var payload syncjob.ContactsSyncPayload
		if err := decodePayload(envelope.Payload, &payload); err != nil {
			return Permanent(err)
		}
		return p.handleContacts(ctx, envelope, payload)
	case syncjob.KindChatHistory:
		var payload syncjob.ChatHistoryPayload
		if err := decodePayload(envelope.Payload, &payload); err != nil {
			return Permanent(err)
		}
		return p.handleChatHistory(ctx, envelope, payload)
	case syncjob.KindChatMembers:
		var payload syncjob.ChatMembersPayload
		if err := decodePayload(envelope.Payload, &payload); err != nil {
			return Permanent(err)
		}
		return p.handleChatMembers(ctx, envelope, payload)
	case syncjob.KindMediaDownload:
		var payload syncjob.MediaDownloadPayload
		if err := decodePayload(envelope.Payload, &payload); err != nil {
			return Permanent(err)
		}
		return p.handleMediaDownload(ctx, envelope, payload)
	default:
		return Permanent(fmt.Errorf("unsupported job kind %q", envelope.Kind))
	}
}

func (p *Processor) beforeTelegram(ctx context.Context, accountID, method string, methodLimit ratelimit.Limit) error {
	cooldown, err := p.limiter.AccountCooldown(ctx, accountID)
	if err != nil {
		return fmt.Errorf("read Telegram cooldown: %w", err)
	}
	if cooldown > 0 {
		return RetryAfter(cooldown, errors.New("Telegram account is in FLOOD_WAIT cooldown"))
	}

	accountResult, err := p.limiter.Allow(ctx, ratelimit.TelegramAccountKey(accountID), p.policies.TelegramAccount)
	if err != nil {
		return fmt.Errorf("apply Telegram account rate limit: %w", err)
	}
	if !accountResult.Allowed {
		return RetryAfter(minimumRetry(accountResult.RetryAfter), errors.New("Telegram account rate limit reached"))
	}

	methodResult, err := p.limiter.Allow(ctx, ratelimit.TelegramMethodKey(accountID, method), methodLimit)
	if err != nil {
		return fmt.Errorf("apply Telegram method rate limit: %w", err)
	}
	if !methodResult.Allowed {
		return RetryAfter(minimumRetry(methodResult.RetryAfter), errors.New("Telegram method rate limit reached"))
	}
	return nil
}

func (p *Processor) telegramError(ctx context.Context, accountID string, err error) error {
	if err == nil {
		return nil
	}
	if retryAfter, ok := telegram.AsFloodWait(err); ok {
		// Small safety margin keeps us from retrying at the exact Telegram boundary.
		retryAfter += time.Second
		stored, cooldownErr := p.limiter.SetAccountCooldown(ctx, accountID, retryAfter)
		if cooldownErr != nil {
			return fmt.Errorf("persist Telegram FLOOD_WAIT cooldown after %v: %w", err, cooldownErr)
		}
		return RetryAfter(stored, err)
	}
	return err
}

func (p *Processor) writeAudit(ctx context.Context, accountID, action, resourceType, resourceID string, metadata map[string]any) error {
	return p.audit.Write(ctx, audit.Event{
		AccountID:    &accountID,
		ActorType:    audit.ActorSystem,
		ActorID:      p.workerID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Metadata:     metadata,
	})
}

func decodePayload(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return errors.New("job payload is empty")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode job payload: %w", err)
	}
	return nil
}

func minimumRetry(value time.Duration) time.Duration {
	if value < time.Second {
		return time.Second
	}
	return value
}
