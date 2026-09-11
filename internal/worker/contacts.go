package worker

import (
	"context"

	"github.com/JavoxirJava/telegram-gateway/internal/contacts"
	"github.com/JavoxirJava/telegram-gateway/internal/syncjob"
)

func (p *Processor) handleContacts(ctx context.Context, envelope syncjob.Envelope, _ syncjob.ContactsSyncPayload) error {
	lease, err := p.acquireSyncLease(ctx, envelope.AccountID, nil, "contacts")
	if err != nil {
		return err
	}
	if err := p.beforeTelegram(ctx, envelope.AccountID, "list_contacts", p.policies.Search); err != nil {
		return p.failSync(ctx, lease, err)
	}

	session, err := p.sessions.Get(ctx, envelope.AccountID)
	if err != nil {
		return p.failSync(ctx, lease, err)
	}
	items, err := session.ListContacts(ctx)
	if err != nil {
		return p.failSync(ctx, lease, p.telegramError(ctx, envelope.AccountID, err))
	}

	seen := make([]int64, 0, len(items))
	for _, item := range items {
		if _, err := p.contacts.Upsert(ctx, envelope.AccountID, contacts.Contact{
			TelegramUserID: item.TelegramUserID,
			FirstName:      optionalString(item.FirstName),
			LastName:       optionalString(item.LastName),
			Username:       optionalString(item.Username),
			IsMutual:       item.IsMutual,
			Metadata:       item.Metadata,
		}, item.PhoneHash); err != nil {
			return p.failSync(ctx, lease, err)
		}
		seen = append(seen, item.TelegramUserID)
	}

	removed, err := p.contacts.MarkMissingDeleted(ctx, envelope.AccountID, seen)
	if err != nil {
		return p.failSync(ctx, lease, err)
	}
	if err := p.syncStates.Progress(ctx, lease, map[string]any{"count": len(items)}, nil, nil); err != nil {
		return p.failSync(ctx, lease, err)
	}
	if err := p.syncStates.Complete(ctx, lease); err != nil {
		return err
	}
	return p.writeAudit(ctx, envelope.AccountID, "TELEGRAM_CONTACTS_SYNCED", "telegram_account", envelope.AccountID, map[string]any{
		"count":        len(items),
		"soft_deleted": removed,
	})
}
