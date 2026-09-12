package accountsync

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"

	"github.com/JavoxirJava/telegram-gateway/internal/media"
	"github.com/JavoxirJava/telegram-gateway/internal/tdadapter"
	"github.com/jackc/pgx/v5"
)

func (r *Runner) download(ctx context.Context, j Job) error {
	item, err := media.NewRepository(r.queue.pool).GetActive(ctx, r.queue.lease.AccountID, j.Payload.MediaID)
	if errors.Is(err, pgx.ErrNoRows) {
		return r.queue.Finish(ctx, j, nil)
	}
	if err != nil {
		return err
	}
	if item.DownloadStatus == "ready" {
		return r.queue.Finish(ctx, j, nil)
	}
	var chatID, messageID int64
	if err := r.queue.pool.QueryRow(ctx, `SELECT c.telegram_chat_id,m.telegram_message_id FROM active_messages m JOIN active_chats c ON c.id=m.chat_id WHERE m.id=$1::uuid AND m.account_id=$2::uuid`, item.MessageID, r.queue.lease.AccountID).Scan(&chatID, &messageID); err != nil {
		return err
	}
	fresh, err := r.native.Message(ctx, chatID, messageID)
	if err != nil {
		return err
	}
	fileID := int64(0)
	for _, f := range fresh.Media {
		if f.Type == item.MediaType && item.UniqueFileKey != nil && *item.UniqueFileKey != "" && *item.UniqueFileKey == f.UniqueFileKey {
			fileID = f.TelegramFileID
			break
		}
	}
	if fileID == 0 {
		// Old/native-DB IDs can be reused. Never download by a guessed old number.
		return r.queue.Finish(ctx, j, func(c context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(c, `UPDATE message_media SET retired=TRUE WHERE id=$1::uuid`, item.ID); err != nil {
				return err
			}
			return r.queue.enqueue(c, tx, "refresh", Payload{ChatID: item.ChatID, TelegramChatID: chatID, MessageID: messageID}, true)
		})
	}
	dl, err := r.native.DownloadFile(ctx, fileID)
	if err != nil {
		return err
	}
	defer dl.Reader.Close()
	key, err := media.ObjectKey(r.queue.lease.AccountID, item.ID)
	if err != nil {
		return err
	}
	// Each claim writes a new immutable version. A stale native worker cannot
	// overwrite an object that a newer owner has already published in PostgreSQL.
	key += "/versions/" + j.Token
	hash := sha256.New()
	reader := io.TeeReader(dl.Reader, hash)
	info, err := r.objects.Put(ctx, key, reader, dl.Size, "application/octet-stream")
	if err != nil {
		return err
	}
	if info.Size != dl.Size {
		return tdadapter.ErrFileSize
	}
	return r.queue.Finish(ctx, j, func(c context.Context, tx pgx.Tx) error {
		// Revalidate at publication. Private orphan versions are never exposed.
		var id string
		if err := tx.QueryRow(c, `SELECT id::text FROM active_message_media WHERE id=$1::uuid AND account_id=$2::uuid AND unique_file_key=$3`, item.ID, r.queue.lease.AccountID, item.UniqueFileKey).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		_, err := tx.Exec(c, `UPDATE message_media SET object_key=$2,download_status='ready',file_size=$3,sha256=$4,downloaded_at=NOW(),last_error=NULL,updated_at=NOW() WHERE id=$1::uuid`, item.ID, key, dl.Size, hash.Sum(nil))
		return err
	})
}
