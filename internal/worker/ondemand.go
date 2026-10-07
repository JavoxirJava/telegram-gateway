package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/chats"
	"github.com/JavoxirJava/telegram-gateway/internal/members"
	"github.com/JavoxirJava/telegram-gateway/internal/syncjob"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/jackc/pgx/v5/pgxpool"
)

type onDemandKey struct{}

// OnDemand performs bounded work in the caller's context, never queued work.
// Account serialization prevents overlapping snapshots and Telegram bursts.
type OnDemand struct {
	p     *Processor
	pool  *pgxpool.Pool
	mu    sync.Mutex
	locks map[string]chan struct{}
}

func NewOnDemand(p *Processor, pool *pgxpool.Pool) *OnDemand {
	return &OnDemand{p: p, pool: pool, locks: map[string]chan struct{}{}}
}

func (d *OnDemand) Refresh(ctx context.Context, account string, request telegram.ReadRequest) (telegram.ReadResult, error) {
	ctx, cancel := context.WithTimeout(context.WithValue(ctx, onDemandKey{}, true), 45*time.Second)
	defer cancel()
	d.mu.Lock()
	lock := d.locks[account]
	if lock == nil {
		lock = make(chan struct{}, 1)
		d.locks[account] = lock
	}
	d.mu.Unlock()
	select {
	case lock <- struct{}{}:
		defer func() { <-lock }()
	case <-ctx.Done():
		return telegram.ReadResult{}, ctx.Err()
	}
	result, err := d.refresh(ctx, account, request)
	if err != nil {
		return result, err
	}
	err = d.p.writeAudit(ctx, account, "TELEGRAM_ON_DEMAND_READ", "telegram_account", account, map[string]any{"kind": request.Kind})
	return result, err
}

func (d *OnDemand) refresh(ctx context.Context, account string, request telegram.ReadRequest) (telegram.ReadResult, error) {
	result := telegram.ReadResult{MessageIDs: []string{}}
	if _, err := d.p.accounts.GetActive(ctx, account); err != nil {
		return result, err
	}
	if request.Limit < 1 || request.Limit > 100 {
		request.Limit = 30
	}
	// Resolve chat/media ownership before requesting anything from Telegram.
	var tgChat int64
	if request.ChatID != "" {
		if err := d.pool.QueryRow(ctx, `SELECT telegram_chat_id FROM active_chats WHERE account_id=$1::uuid AND id=$2::uuid`, account, request.ChatID).Scan(&tgChat); err != nil {
			return result, err
		}
	}
	if request.Kind == "media" {
		item, err := d.p.mediaRepo.GetActive(ctx, account, request.MediaID)
		if err != nil {
			return result, err
		}
		if item.TelegramFileID == nil {
			return result, errors.New("media source unavailable")
		}
		return result, d.p.handleMediaDownload(ctx, syncjob.Envelope{AccountID: account}, syncjob.MediaDownloadPayload{MediaID: item.ID, MessageID: item.MessageID, TelegramFileID: *item.TelegramFileID})
	}
	session, err := d.p.sessions.Get(ctx, account)
	if err != nil {
		return result, err
	}
	call := func(method string) error {
		policy := d.p.policies.History
		if method == "search_chats" || method == "search_messages" {
			policy = d.p.policies.Search
		}
		for {
			err := d.p.beforeTelegram(ctx, account, method, policy)
			delay, retry := RetryDelay(err)
			if !retry {
				return err
			}
			deadline, _ := ctx.Deadline()
			if time.Until(deadline) <= delay {
				return err
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	switch request.Kind {
	case "profile":
		if err := call("profile"); err != nil {
			return result, err
		}
		profile, err := session.Profile(ctx)
		if err != nil {
			return result, d.p.telegramError(ctx, account, err)
		}
		_, err = d.p.accounts.Activate(ctx, account, profile.TelegramUserID, profile.DisplayName, profile.Username)
		return result, err
	case "search_chats":
		searcher, ok := session.(telegram.ChatSearcher)
		if !ok {
			return result, errors.New("Telegram chat search unavailable")
		}
		if err := call("search_chats"); err != nil {
			return result, err
		}
		items, err := searcher.SearchChats(ctx, request.Query, request.Limit)
		if err != nil {
			return result, d.p.telegramError(ctx, account, err)
		}
		for _, item := range items {
			id, err := d.storeChat(ctx, account, item)
			if err != nil {
				return result, err
			}
			result.ChatIDs = append(result.ChatIDs, id)
		}
		return result, nil
	case "chats":
		// Exactly one native page; return its continuation without fetching it.
		// Short/empty pages (e.g. the main/archive boundary) can still have a cursor.
		if _, _, err := telegram.ParseChatCursor(request.Cursor); err != nil {
			return result, err
		}
		for {
			if err := call("list_chats"); err != nil {
				return result, err
			}
			page, err := session.ListChats(ctx, request.Cursor, request.Limit)
			if errors.Is(err, telegram.ErrChatListLoading) {
				timer := time.NewTimer(150 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return result, ctx.Err()
				case <-timer.C:
				}
				continue
			}
			if err != nil {
				return result, d.p.telegramError(ctx, account, err)
			}
			for _, item := range page.Items {
				id, err := d.storeChat(ctx, account, item)
				if err != nil {
					return result, err
				}
				result.ChatIDs = append(result.ChatIDs, id)
			}
			if page.NextCursor != "" && page.NextCursor == request.Cursor {
				return result, errors.New("chat pagination did not advance")
			}
			result.NextCursor = page.NextCursor
			return result, nil
		}
	case "contacts":
		return result, d.p.handleContacts(ctx, syncjob.Envelope{AccountID: account}, syncjob.ContactsSyncPayload{})
	case "members":
		started := time.Now().UTC().Truncate(time.Microsecond)
		cursor := ""
		for {
			if err := call("list_members"); err != nil {
				return result, err
			}
			page, err := session.ListMembers(ctx, tgChat, cursor, 200)
			if err != nil {
				return result, d.p.telegramError(ctx, account, err)
			}
			for _, item := range page.Items {
				_, err := d.p.members.Upsert(ctx, account, request.ChatID, members.Member{PeerType: item.PeerType, TelegramPeerID: item.TelegramPeerID, FirstName: optionalString(item.FirstName), LastName: optionalString(item.LastName), Username: optionalString(item.Username), Role: optionalString(item.Role), Metadata: item.Metadata})
				if err != nil {
					return result, err
				}
			}
			if page.NextCursor == "" {
				_, err := d.p.members.MarkNotSeenSince(ctx, account, request.ChatID, started)
				return result, err
			}
			if page.NextCursor == cursor {
				return result, errors.New("member pagination did not advance")
			}
			cursor = page.NextCursor
		}
	case "messages":
		before := request.BeforeMessageID
		for len(result.MessageIDs) < request.Limit+1 {
			if err := call("chat_history"); err != nil {
				return result, err
			}
			count := min(100, request.Limit+1-len(result.MessageIDs))
			// TDLib includes from_message_id; leave room for it on subsequent pages.
			if before != 0 {
				count = min(100, count+1)
			}
			items, err := session.GetChatHistory(ctx, tgChat, before, count)
			if err != nil {
				return result, d.p.telegramError(ctx, account, err)
			}
			if len(items) == 0 {
				return result, nil
			}
			next := before
			for _, item := range items {
				if item.TelegramMessageID == 0 || (before != 0 && item.TelegramMessageID >= before) {
					continue
				}
				id, err := d.storeMessage(ctx, account, request.ChatID, item)
				if err != nil {
					return result, err
				}
				result.MessageIDs = append(result.MessageIDs, id)
				if next == 0 || item.TelegramMessageID < next {
					next = item.TelegramMessageID
				}
				if len(result.MessageIDs) == request.Limit+1 {
					break
				}
			}
			if next == before {
				return result, errors.New("history pagination did not advance")
			}
			before = next
		}
		return result, nil
	case "search_messages":
		if request.ChatID == "" {
			return result, errors.New("chat_id is required")
		}
		searcher, ok := session.(telegram.ChatMessageSearcher)
		if !ok {
			return result, errors.New("Telegram chat message search unavailable")
		}
		if err := call("search_messages"); err != nil {
			return result, err
		}
		items, err := searcher.SearchChatMessages(ctx, tgChat, request.Query, request.Limit)
		if err != nil {
			return result, d.p.telegramError(ctx, account, err)
		}
		for _, item := range items {
			id, err := d.storeMessage(ctx, account, request.ChatID, item)
			if err != nil {
				return result, err
			}
			result.MessageIDs = append(result.MessageIDs, id)
		}
		return result, nil
	default:
		return result, errors.New("unsupported on-demand read")
	}
}

func (d *OnDemand) storeChat(ctx context.Context, account string, c telegram.Chat) (string, error) {
	return d.p.chats.Upsert(ctx, chats.Chat{AccountID: account, TelegramChatID: c.TelegramChatID, ChatType: c.Type, Title: optionalString(c.Title), Username: optionalString(c.Username), Metadata: c.Metadata, LastMessageID: c.LastMessageID, LastMessageAt: c.LastMessageAt})
}
func (d *OnDemand) storeMessage(ctx context.Context, account, chat string, item telegram.Message) (string, error) {
	if err := d.p.StoreMessage(ctx, account, chat, item); err != nil {
		return "", err
	}
	var id string
	err := d.pool.QueryRow(ctx, `SELECT id::text FROM messages WHERE account_id=$1::uuid AND chat_id=$2::uuid AND telegram_message_id=$3`, account, chat, item.TelegramMessageID).Scan(&id)
	return id, err
}
