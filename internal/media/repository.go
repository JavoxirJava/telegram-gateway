package media

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Item struct {
	ID             string
	MessageID      string
	AccountID      string
	ChatID         string
	MediaType      string
	TelegramFileID *int64
	UniqueFileKey  *string
	ObjectKey      *string
	MIMEType       *string
	FileName       *string
	FileSize       *int64
	SHA256         []byte
	DownloadStatus string
	AttemptCount   int
	LastAttemptAt  *time.Time
	DownloadedAt   *time.Time
	LastError      *string
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) RegisterPending(ctx context.Context, messageID, mediaType string, telegramFileID *int64, uniqueFileKey, mimeType, fileName *string, fileSize *int64) (string, error) {
	messageID = strings.TrimSpace(messageID)
	mediaType = strings.TrimSpace(mediaType)
	if messageID == "" || mediaType == "" {
		return "", errors.New("message id and media type are required")
	}
	if fileSize != nil && *fileSize < 0 {
		return "", errors.New("file size cannot be negative")
	}

	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO message_media (
			message_id, media_type, telegram_file_id, unique_file_key,
			mime_type, file_name, file_size, download_status
		) VALUES (
			$1::uuid, $2, $3, $4, $5, $6, $7, 'pending'
		)
		RETURNING id::text`,
		messageID, mediaType, telegramFileID, trimOptional(uniqueFileKey), trimOptional(mimeType), trimOptional(fileName), fileSize,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("register message media: %w", err)
	}
	return id, nil
}

func (r *Repository) ClaimDownload(ctx context.Context, mediaID string, maxAttempts int) (bool, error) {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	result, err := r.pool.Exec(ctx, `
		UPDATE message_media
		SET download_status = 'downloading',
		    attempt_count = attempt_count + 1,
		    last_attempt_at = NOW(),
		    last_error = NULL,
		    updated_at = NOW()
		WHERE id = $1::uuid
		  AND download_status IN ('pending', 'failed')
		  AND attempt_count < $2`, strings.TrimSpace(mediaID), maxAttempts)
	if err != nil {
		return false, fmt.Errorf("claim media download: %w", err)
	}
	return result.RowsAffected() == 1, nil
}

func (r *Repository) MarkReady(ctx context.Context, mediaID, objectKey, mimeType string, fileSize int64, sha256 []byte) error {
	mediaID = strings.TrimSpace(mediaID)
	objectKey = strings.TrimSpace(objectKey)
	if mediaID == "" || objectKey == "" {
		return errors.New("media id and object key are required")
	}
	if fileSize < 0 {
		return errors.New("file size cannot be negative")
	}

	result, err := r.pool.Exec(ctx, `
		UPDATE message_media
		SET object_key = $2,
		    mime_type = COALESCE(NULLIF($3, ''), mime_type),
		    file_size = $4,
		    sha256 = $5,
		    download_status = 'ready',
		    downloaded_at = NOW(),
		    last_error = NULL,
		    updated_at = NOW()
		WHERE id = $1::uuid
		  AND download_status = 'downloading'`, mediaID, objectKey, strings.TrimSpace(mimeType), fileSize, sha256)
	if err != nil {
		return fmt.Errorf("mark media ready: %w", err)
	}
	if result.RowsAffected() == 0 {
		return errors.New("media download is not owned or no longer downloading")
	}
	return nil
}

func (r *Repository) MarkFailed(ctx context.Context, mediaID string, cause error) error {
	message := "media download failed"
	if cause != nil {
		message = cause.Error()
	}
	if len(message) > 4000 {
		message = message[:4000]
	}
	result, err := r.pool.Exec(ctx, `
		UPDATE message_media
		SET download_status = 'failed',
		    last_error = $2,
		    updated_at = NOW()
		WHERE id = $1::uuid
		  AND download_status = 'downloading'`, strings.TrimSpace(mediaID), message)
	if err != nil {
		return fmt.Errorf("mark media failed: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *Repository) GetActive(ctx context.Context, accountID, mediaID string) (Item, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id::text, message_id::text, account_id::text, chat_id::text,
		       media_type, telegram_file_id, unique_file_key, object_key,
		       mime_type, file_name, file_size, sha256, download_status,
		       attempt_count, last_attempt_at, downloaded_at, last_error
		FROM active_message_media
		WHERE account_id = $1::uuid
		  AND id = $2::uuid`, strings.TrimSpace(accountID), strings.TrimSpace(mediaID))
	return scanItem(row)
}

func (r *Repository) GetForWorker(ctx context.Context, accountID, mediaID string) (Item, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT mm.id::text, mm.message_id::text, m.account_id::text, m.chat_id::text,
		       mm.media_type, mm.telegram_file_id, mm.unique_file_key, mm.object_key,
		       mm.mime_type, mm.file_name, mm.file_size, mm.sha256, mm.download_status,
		       mm.attempt_count, mm.last_attempt_at, mm.downloaded_at, mm.last_error
		FROM message_media mm
		JOIN messages m ON m.id = mm.message_id
		WHERE m.account_id = $1::uuid
		  AND mm.id = $2::uuid`, strings.TrimSpace(accountID), strings.TrimSpace(mediaID))
	return scanItem(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanItem(row rowScanner) (Item, error) {
	var item Item
	if err := row.Scan(
		&item.ID,
		&item.MessageID,
		&item.AccountID,
		&item.ChatID,
		&item.MediaType,
		&item.TelegramFileID,
		&item.UniqueFileKey,
		&item.ObjectKey,
		&item.MIMEType,
		&item.FileName,
		&item.FileSize,
		&item.SHA256,
		&item.DownloadStatus,
		&item.AttemptCount,
		&item.LastAttemptAt,
		&item.DownloadedAt,
		&item.LastError,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Item{}, pgx.ErrNoRows
		}
		return Item{}, fmt.Errorf("scan media item: %w", err)
	}
	return item, nil
}

func trimOptional(value *string) any {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}
