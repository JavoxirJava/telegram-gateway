// Package livepg atomically applies normalized live Telegram updates and their
// audit event. It never stores credentials or raw TDLib update bodies.
package livepg

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	sessionruntime "github.com/JavoxirJava/telegram-gateway/internal/runtime"
	"github.com/JavoxirJava/telegram-gateway/internal/tglive"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrLeaseLost = errors.New("live update session lease lost")

type Store struct {
	lease *sessionruntime.Lease
	pool  *pgxpool.Pool
	audit *audit.Writer
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool, audit: audit.NewWriter(pool)} }

// NewWithLease fences every database transaction against current ownership.
func NewWithLease(pool *pgxpool.Pool, lease sessionruntime.Lease) *Store {
	store := New(pool)
	store.lease = &lease
	return store
}

func (s *Store) Apply(ctx context.Context, accountID string, event tglive.Event) error {
	if event.ChatID == 0 {
		return errors.New("chat identity is required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if s.lease != nil {
		lease := s.lease
		if accountID != lease.AccountID {
			return ErrLeaseLost
		}
		var marker int
		err := tx.QueryRow(ctx, `SELECT 1 FROM telegram_session_runtime r JOIN telegram_accounts a ON a.id=r.account_id
   WHERE r.account_id=$1::uuid AND r.worker_id=$2 AND r.lease_token=$3::uuid AND r.generation=$4
    AND r.desired_state='online' AND r.lease_expires_at > clock_timestamp() AND a.status IN ('pending','active')
   FOR SHARE OF r`, lease.AccountID, lease.WorkerID, lease.Token, lease.Generation).Scan(&marker)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLeaseLost
		}
		if err != nil {
			return err
		}
	}

	var chatID string
	if event.Kind == "chat" {
		err = tx.QueryRow(ctx, `INSERT INTO chats(account_id,telegram_chat_id,chat_type,title) VALUES($1::uuid,$2,$3,$4)
   ON CONFLICT(account_id,telegram_chat_id) DO UPDATE SET title=EXCLUDED.title,chat_type=EXCLUDED.chat_type,updated_at=NOW()
   RETURNING id::text`, accountID, event.ChatID, event.ChatType, event.Title).Scan(&chatID)
	} else {
		err = tx.QueryRow(ctx, `SELECT id::text FROM chats WHERE account_id=$1::uuid AND telegram_chat_id=$2`, accountID, event.ChatID).Scan(&chatID)
	}
	if err != nil {
		return fmt.Errorf("resolve live chat: %w", err)
	}
	// Same key used by the message repository. Acquisition precedes row mutation.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::uuid::text || ':' || $2::uuid::text,11))`, accountID, chatID); err != nil {
		return err
	}
	switch event.Kind {
	case "chat":
	case "title":
		_, err = tx.Exec(ctx, `UPDATE chats SET title=$3,updated_at=NOW() WHERE id=$1::uuid AND account_id=$2::uuid`, chatID, accountID, event.Title)
	case "message":
		_, err = tx.Exec(ctx, `INSERT INTO messages(account_id,chat_id,telegram_message_id,sender_telegram_id,sender_chat_id,
   message_type,content,content_entities,sent_at,edited_at,live_content_version)
   VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,1)
   ON CONFLICT(account_id,chat_id,telegram_message_id) DO UPDATE SET
   message_type=EXCLUDED.message_type,content=EXCLUDED.content,content_entities=EXCLUDED.content_entities,
   edited_at=EXCLUDED.edited_at,live_content_version=messages.live_content_version+1,updated_at=NOW()`,
			accountID, chatID, event.MessageID, event.SenderUserID, event.SenderChatID, event.ContentType, event.Content, string(event.Entities), event.SentAt, event.EditedAt)
	case "content":
		_, err = tx.Exec(ctx, `UPDATE messages SET message_type=$4,content=$5,content_entities=$6::jsonb,
   live_content_version=live_content_version+1,updated_at=NOW()
   WHERE account_id=$1::uuid AND chat_id=$2::uuid AND telegram_message_id=$3`, accountID, chatID, event.MessageID, event.ContentType, event.Content, string(event.Entities))
	case "edited":
		_, err = tx.Exec(ctx, `UPDATE messages SET edited_at=GREATEST(edited_at,$4::timestamptz),updated_at=NOW(),live_content_version=live_content_version+1
   WHERE account_id=$1::uuid AND chat_id=$2::uuid AND telegram_message_id=$3`, accountID, chatID, event.MessageID, event.EditedAt)
	case "deleted":
		_, err = tx.Exec(ctx, `INSERT INTO telegram_message_tombstones(account_id,telegram_chat_id,telegram_message_id)
   SELECT $1::uuid,$2,unnest($3::bigint[]) ON CONFLICT DO NOTHING`, accountID, event.ChatID, event.MessageIDs)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE messages SET deleted=TRUE,deleted_at=COALESCE(deleted_at,NOW()),updated_at=NOW()
    WHERE account_id=$1::uuid AND chat_id=$2::uuid AND telegram_message_id=ANY($3::bigint[])`, accountID, chatID, event.MessageIDs)
		}
	default:
		return errors.New("unsupported normalized update")
	}
	if err != nil {
		return fmt.Errorf("apply live update: %w", err)
	}
	if err := s.audit.WriteTx(ctx, tx, audit.Event{AccountID: &accountID, ActorType: audit.ActorTelegram, Action: "TELEGRAM_LIVE_" + event.Kind, ResourceType: "chat", ResourceID: chatID,
		Metadata: map[string]any{"telegram_chat_id": strconv.FormatInt(event.ChatID, 10), "message_id": strconv.FormatInt(event.MessageID, 10), "deletion_count": len(event.MessageIDs)}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
