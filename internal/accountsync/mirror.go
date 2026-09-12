package accountsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/chats"
	"github.com/JavoxirJava/telegram-gateway/internal/members"
	"github.com/JavoxirJava/telegram-gateway/internal/messages"
	"github.com/JavoxirJava/telegram-gateway/internal/tdadapter"
	"github.com/JavoxirJava/telegram-gateway/internal/tdjson"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/jackc/pgx/v5"
)

func optional(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
func (r *Runner) chats(ctx context.Context, j Job) error {
	page, err := r.native.ListChats(ctx, j.Payload.Cursor, 100)
	if err != nil {
		return err
	}
	return r.queue.Finish(ctx, j, func(c context.Context, tx pgx.Tx) error {
		for _, v := range page.Items {
			id, err := chats.NewTx(tx).Upsert(c, chats.Chat{AccountID: r.queue.lease.AccountID, TelegramChatID: v.TelegramChatID, ChatType: v.Type, Title: optional(v.Title), Username: optional(v.Username), Metadata: v.Metadata})
			if err != nil {
				return err
			}
			var before int64
			var exhausted bool
			err = tx.QueryRow(c, `SELECT before_message_id,exhausted FROM gateway_history_progress WHERE account_id=$1::uuid AND chat_id=$2::uuid`, r.queue.lease.AccountID, id).Scan(&before, &exhausted)
			known := err == nil
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			var latest int64
			if known {
				if err := tx.QueryRow(c, `SELECT COALESCE(MAX(telegram_message_id),0) FROM messages WHERE account_id=$1::uuid AND chat_id=$2::uuid`, r.queue.lease.AccountID, id).Scan(&latest); err != nil {
					return err
				}
			}
			p := Payload{ChatID: id, TelegramChatID: v.TelegramChatID, Cycle: j.Payload.Cycle, StopAt: latest}
			if err := r.queue.enqueue(c, tx, "history", p, false); err != nil {
				return err
			}
			if known && !exhausted && before > 0 {
				backfill := p
				backfill.Before = before
				backfill.StopAt = 0
				backfill.Cycle = "backfill"
				if err := r.queue.enqueue(c, tx, "history", backfill, false); err != nil {
					return err
				}
			}
			if j.Payload.Cycle == "startup" || strings.HasPrefix(j.Payload.Cycle, "reconnect-") {
				if err := r.queue.enqueue(c, tx, "reconcile", Payload{ChatID: id, TelegramChatID: v.TelegramChatID, Cycle: j.Payload.Cycle}, false); err != nil {
					return err
				}
				if v.Type == "group" || v.Type == "supergroup" || v.Type == "channel" {
					if err := r.queue.enqueue(c, tx, "members", Payload{ChatID: id, TelegramChatID: v.TelegramChatID, Cycle: j.Payload.Cycle, Snapshot: time.Now().UTC()}, false); err != nil {
						return err
					}
				}
			}
		}
		if page.NextCursor != "" {
			p := j.Payload
			p.Cursor = page.NextCursor
			return r.queue.enqueue(c, tx, "chats", p, false)
		}
		return r.queue.enqueue(c, tx, "contacts", Payload{Cycle: j.Payload.Cycle}, false)
	})
}
func (r *Runner) validChat(ctx context.Context, p Payload) error {
	if p.ChatID == "" || p.TelegramChatID == 0 {
		return tdadapter.ErrInvalidResponse
	}
	var one int
	err := r.queue.pool.QueryRow(ctx, `SELECT 1 FROM active_chats WHERE id=$1::uuid AND account_id=$2::uuid AND telegram_chat_id=$3`, p.ChatID, r.queue.lease.AccountID, p.TelegramChatID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return tdadapter.ErrExcluded
	}
	return err
}
func (r *Runner) history(ctx context.Context, j Job) error {
	if err := r.validChat(ctx, j.Payload); err != nil {
		return err
	}
	page, err := r.native.GetChatHistory(ctx, j.Payload.TelegramChatID, j.Payload.Before, 100)
	if err != nil {
		return err
	}
	if err = page.Validate(j.Payload.Before, 100); err != nil {
		return tdadapter.ErrInvalidResponse
	}
	return r.queue.Finish(ctx, j, func(c context.Context, tx pgx.Tx) error {
		for _, v := range page.Items {
			id, err := messages.NewTx(tx).Upsert(c, toMessage(r.queue.lease.AccountID, j.Payload.ChatID, v))
			if err != nil {
				return err
			}
			var version int64
			var blocked bool
			if err = tx.QueryRow(c, `SELECT live_content_version,(deleted OR access_blocked) FROM messages WHERE id=$1::uuid`, id).Scan(&version, &blocked); err != nil {
				return err
			}
			if blocked {
				continue
			}
			if version > 0 {
				if err = r.queue.enqueue(c, tx, "refresh", Payload{ChatID: j.Payload.ChatID, TelegramChatID: j.Payload.TelegramChatID, MessageID: v.TelegramMessageID}, true); err != nil {
					return err
				}
			} else if err = r.attachments(c, tx, id, v.Media); err != nil {
				return err
			}
		}
		if j.Payload.StopAt == 0 {
			_, err := tx.Exec(c, `INSERT INTO gateway_history_progress(account_id,chat_id,before_message_id,exhausted)
   VALUES($1::uuid,$2::uuid,$3,$4) ON CONFLICT(account_id,chat_id) DO UPDATE SET
   before_message_id=CASE WHEN EXCLUDED.before_message_id=0 THEN gateway_history_progress.before_message_id WHEN gateway_history_progress.before_message_id=0 THEN EXCLUDED.before_message_id ELSE LEAST(gateway_history_progress.before_message_id,EXCLUDED.before_message_id) END,
   exhausted=gateway_history_progress.exhausted OR EXCLUDED.exhausted,checked_at=NOW()`, r.queue.lease.AccountID, j.Payload.ChatID, page.NextBeforeMessageID, page.Exhausted)
			if err != nil {
				return err
			}
		}
		if !page.Exhausted && (j.Payload.StopAt == 0 || page.NextBeforeMessageID > j.Payload.StopAt) {
			p := j.Payload
			p.Before = page.NextBeforeMessageID
			return r.queue.enqueue(c, tx, "history", p, false)
		}
		return nil
	})
}
func toMessage(account, chat string, v telegram.Message) messages.Message {
	return messages.Message{AccountID: account, ChatID: chat, TelegramMessageID: v.TelegramMessageID, SenderTelegramID: v.SenderTelegramID, SenderChatID: v.SenderChatID, MessageType: v.Type, Content: v.Content, ContentEntities: v.Entities, ReplyToMessageID: v.ReplyToMessageID, SentAt: v.SentAt, EditedAt: v.EditedAt}
}
func (r *Runner) contacts(ctx context.Context, j Job) error {
	items, err := r.native.ListContacts(ctx)
	if err != nil {
		return err
	}
	if len(items) > 100000 {
		return tdadapter.ErrInvalidResponse
	}
	// A single bounded SQL statement avoids 100,000 network round trips/locks.
	type contact struct {
		ID       int64  `json:"id"`
		First    string `json:"first"`
		Last     string `json:"last"`
		Username string `json:"username"`
		Mutual   bool   `json:"mutual"`
	}
	data := make([]contact, 0, len(items))
	seen := map[int64]bool{}
	for _, v := range items {
		if v.TelegramUserID <= 0 || seen[v.TelegramUserID] {
			return tdadapter.ErrInvalidResponse
		}
		seen[v.TelegramUserID] = true
		data = append(data, contact{v.TelegramUserID, v.FirstName, v.LastName, v.Username, v.IsMutual})
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return r.queue.Finish(ctx, j, func(c context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(c, `INSERT INTO telegram_contacts(account_id,telegram_user_id,first_name,last_name,username,is_mutual)
   SELECT $1::uuid,x.id,NULLIF(x.first,''),NULLIF(x.last,''),NULLIF(x.username,''),x.mutual
   FROM jsonb_to_recordset($2::jsonb) x(id bigint,first text,last text,username text,mutual boolean)
   ON CONFLICT(account_id,telegram_user_id) DO UPDATE SET first_name=EXCLUDED.first_name,last_name=EXCLUDED.last_name,username=EXCLUDED.username,is_mutual=EXCLUDED.is_mutual,deleted=FALSE,deleted_at=NULL,updated_at=NOW()`, r.queue.lease.AccountID, string(raw)); err != nil {
			return err
		}
		_, err := tx.Exec(c, `UPDATE telegram_contacts SET deleted=TRUE,deleted_at=COALESCE(deleted_at,NOW()),updated_at=NOW()
   WHERE account_id=$1::uuid AND NOT deleted AND telegram_user_id NOT IN (SELECT x.id FROM jsonb_to_recordset($2::jsonb) x(id bigint))`, r.queue.lease.AccountID, string(raw))
		return err
	})
}
func (r *Runner) members(ctx context.Context, j Job) error {
	if err := r.validChat(ctx, j.Payload); err != nil {
		return err
	}
	page, err := r.native.ListMembers(ctx, j.Payload.TelegramChatID, j.Payload.Cursor, 200)
	if err != nil {
		return err
	}
	return r.queue.Finish(ctx, j, func(c context.Context, tx pgx.Tx) error {
		repo := members.NewTx(tx)
		for _, v := range page.Items {
			if _, err := repo.Upsert(c, r.queue.lease.AccountID, j.Payload.ChatID, members.Member{PeerType: v.PeerType, TelegramPeerID: v.TelegramPeerID, FirstName: optional(v.FirstName), LastName: optional(v.LastName), Username: optional(v.Username), Role: optional(v.Role)}); err != nil {
				return err
			}
		}
		if page.NextCursor != "" {
			p := j.Payload
			p.Cursor = page.NextCursor
			return r.queue.enqueue(c, tx, "members", p, false)
		}
		if j.Payload.Snapshot.IsZero() {
			return tdadapter.ErrInvalidResponse
		}
		_, err := repo.MarkNotSeenSince(c, r.queue.lease.AccountID, j.Payload.ChatID, j.Payload.Snapshot)
		return err
	})
}
func (r *Runner) attachments(ctx context.Context, tx pgx.Tx, messageID string, items []telegram.Media) error {
	if _, err := tx.Exec(ctx, `UPDATE message_media SET retired=TRUE WHERE message_id=$1::uuid`, messageID); err != nil {
		return err
	}
	for _, v := range items {
		if v.TelegramFileID <= 0 || v.UniqueFileKey == "" {
			continue
		}
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO message_media(message_id,media_type,telegram_file_id,unique_file_key,mime_type,file_name,file_size,source_generation)
   VALUES($1::uuid,$2,$3,NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),$7,$8)
   ON CONFLICT(message_id,media_type,telegram_file_id) WHERE telegram_file_id IS NOT NULL
   DO UPDATE SET retired=FALSE,source_generation=EXCLUDED.source_generation,
    download_status=CASE WHEN message_media.unique_file_key IS NOT NULL AND message_media.unique_file_key=EXCLUDED.unique_file_key THEN message_media.download_status ELSE 'pending' END,
    unique_file_key=EXCLUDED.unique_file_key,mime_type=EXCLUDED.mime_type,file_name=EXCLUDED.file_name,file_size=EXCLUDED.file_size,updated_at=NOW()
   RETURNING id::text`, messageID, v.Type, v.TelegramFileID, v.UniqueFileKey, v.MIMEType, v.FileName, v.FileSize, r.queue.lease.Generation).Scan(&id)
		if err != nil {
			return err
		}
		if err = r.queue.enqueue(ctx, tx, "media", Payload{MediaID: id}, true); err != nil {
			return err
		}
	}
	return nil
}

// applyFresh compares the version recorded BEFORE the RPC. A live edit committed
// during the fetch always wins. The trigger continues to enforce sticky deletes.
func (r *Runner) applyFresh(ctx context.Context, tx pgx.Tx, chatID string, id int64, version int64, v *telegram.Message) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::uuid::text||':'||$2::uuid::text,11))`, r.queue.lease.AccountID, chatID); err != nil {
		return err
	}
	if v == nil {
		_, err := tx.Exec(ctx, `UPDATE messages SET access_blocked=TRUE,updated_at=NOW() WHERE account_id=$1::uuid AND chat_id=$2::uuid AND telegram_message_id=$3 AND live_content_version=$4 AND NOT deleted`, r.queue.lease.AccountID, chatID, id, version)
		return err
	}
	entities, err := json.Marshal(v.Entities)
	if err != nil {
		return err
	}
	if v.Entities == nil {
		entities = []byte("[]")
	}
	var dbid string
	err = tx.QueryRow(ctx, `UPDATE messages SET content=$5,message_type=$6,content_entities=$7::jsonb,edited_at=$8,access_blocked=FALSE,live_content_version=live_content_version+1,updated_at=NOW()
 WHERE account_id=$1::uuid AND chat_id=$2::uuid AND telegram_message_id=$3 AND live_content_version=$4 AND NOT deleted
 AND (edited_at IS NULL OR ($8::timestamptz IS NOT NULL AND edited_at<=$8)) RETURNING id::text`, r.queue.lease.AccountID, chatID, id, version, v.Content, v.Type, string(entities), v.EditedAt).Scan(&dbid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.attachments(ctx, tx, dbid, v.Media)
}
func (r *Runner) refresh(ctx context.Context, j Job) error {
	if err := r.validChat(ctx, j.Payload); err != nil {
		return err
	}
	var version int64
	err := r.queue.pool.QueryRow(ctx, `SELECT live_content_version FROM messages WHERE account_id=$1::uuid AND chat_id=$2::uuid AND telegram_message_id=$3 AND NOT deleted`, r.queue.lease.AccountID, j.Payload.ChatID, j.Payload.MessageID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return r.queue.Finish(ctx, j, nil)
	}
	if err != nil {
		return err
	}
	item, err := r.native.Message(ctx, j.Payload.TelegramChatID, j.Payload.MessageID)
	if err != nil {
		var native *tdjson.Error
		if errors.Is(err, tdadapter.ErrExcluded) || (errors.As(err, &native) && (native.Code == 403 || native.Code == 404)) {
			// Unavailability is reversible; do not invent permanent deletion.
			return r.queue.Finish(ctx, j, func(c context.Context, tx pgx.Tx) error {
				return r.applyFresh(c, tx, j.Payload.ChatID, j.Payload.MessageID, version, nil)
			})
		}
		return err
	}
	return r.queue.Finish(ctx, j, func(c context.Context, tx pgx.Tx) error {
		return r.applyFresh(c, tx, j.Payload.ChatID, j.Payload.MessageID, version, &item)
	})
}
func (r *Runner) reconcile(ctx context.Context, j Job) error {
	if err := r.validChat(ctx, j.Payload); err != nil {
		return err
	}
	rows, err := r.queue.pool.Query(ctx, `SELECT telegram_message_id,live_content_version FROM messages WHERE account_id=$1::uuid AND chat_id=$2::uuid AND NOT deleted AND telegram_message_id>$3 ORDER BY telegram_message_id LIMIT 100`, r.queue.lease.AccountID, j.Payload.ChatID, j.Payload.Before)
	if err != nil {
		return err
	}
	ids := []int64{}
	versions := map[int64]int64{}
	for rows.Next() {
		var id, v int64
		if err := rows.Scan(&id, &v); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
		versions[id] = v
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return r.queue.Finish(ctx, j, nil)
	}
	items, missing, err := r.native.Messages(ctx, j.Payload.TelegramChatID, ids)
	if err != nil {
		return err
	}
	if len(items)+len(missing) != len(ids) {
		return fmt.Errorf("invalid reconciliation batch")
	}
	return r.queue.Finish(ctx, j, func(c context.Context, tx pgx.Tx) error {
		for _, id := range ids {
			var value *telegram.Message
			if v, ok := items[id]; ok {
				value = &v
			}
			if err := r.applyFresh(c, tx, j.Payload.ChatID, id, versions[id], value); err != nil {
				return err
			}
		}
		if len(ids) == 100 {
			p := j.Payload
			p.Before = ids[len(ids)-1]
			return r.queue.enqueue(c, tx, "reconcile", p, false)
		}
		return nil
	})
}
