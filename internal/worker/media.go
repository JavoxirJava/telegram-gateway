package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/syncjob"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
	"github.com/jackc/pgx/v5"
)

const maxMediaDownloadAttempts = 5

func (p *Processor) handleMediaDownload(ctx context.Context, envelope syncjob.Envelope, payload syncjob.MediaDownloadPayload) error {
	if payload.MediaID == "" || payload.MessageID == "" || payload.TelegramFileID == 0 {
		return Permanent(errors.New("media id, message id and Telegram file id are required"))
	}

	item, err := p.mediaRepo.GetForWorker(ctx, envelope.AccountID, payload.MediaID)
	if err != nil {
		return err
	}
	if item.MessageID != payload.MessageID {
		return Permanent(errors.New("media job message id does not match stored media"))
	}
	if item.TelegramFileID == nil {
		return Permanent(errors.New("media has no Telegram file id"))
	}
	if item.DownloadStatus == "ready" {
		return nil
	}
	if item.DownloadStatus == "skipped" {
		return Permanent(errors.New("media is marked as non-downloadable"))
	}
	if item.AttemptCount >= maxMediaDownloadAttempts {
		return Permanent(fmt.Errorf("media download exceeded %d attempts", maxMediaDownloadAttempts))
	}

	if err := p.beforeTelegram(ctx, envelope.AccountID, "download_file", p.policies.Media); err != nil {
		return err
	}
	session, err := p.sessions.Get(ctx, envelope.AccountID)
	if err != nil {
		return RetryAfter(10*time.Second, err)
	}
	claimed, err := p.mediaRepo.ClaimDownloadRecoverable(ctx, payload.MediaID, maxMediaDownloadAttempts)
	if err != nil {
		return err
	}
	if !claimed {
		latest, readErr := p.mediaRepo.GetForWorker(ctx, envelope.AccountID, payload.MediaID)
		if readErr == nil && latest.DownloadStatus == "ready" {
			return nil
		}
		if readErr == nil && latest.AttemptCount >= maxMediaDownloadAttempts {
			return Permanent(fmt.Errorf("media download exceeded %d attempts", maxMediaDownloadAttempts))
		}
		return RetryAfter(30*time.Second, errors.New("media download is currently owned by another worker"))
	}

	fileID := *item.TelegramFileID
	if resolver, ok := session.(telegram.MessageFileResolver); ok {
		chatID, messageID, sourceErr := p.mediaRepo.DownloadSource(ctx, envelope.AccountID, item.ID)
		if sourceErr != nil {
			if errors.Is(sourceErr, pgx.ErrNoRows) {
				sourceErr = Permanent(errors.New("media source message is unavailable"))
			}
			_ = p.mediaRepo.MarkFailed(ctx, item.ID, sourceErr)
			return sourceErr
		}
		unique := ""
		if item.UniqueFileKey != nil {
			unique = *item.UniqueFileKey
		}
		fileID, err = resolver.ResolveMessageFile(ctx, chatID, messageID, unique, item.MediaType)
		if err != nil {
			processed := p.telegramError(ctx, envelope.AccountID, err)
			_ = p.mediaRepo.MarkFailed(ctx, item.ID, processed)
			return processed
		}
	}
	download, err := session.DownloadFile(ctx, fileID)
	if err != nil {
		processed := p.telegramError(ctx, envelope.AccountID, err)
		_ = p.mediaRepo.MarkFailed(ctx, payload.MediaID, processed)
		return processed
	}
	if download.Reader == nil {
		err := errors.New("Telegram download returned no reader")
		_ = p.mediaRepo.MarkFailed(ctx, payload.MediaID, err)
		return err
	}
	defer func() { _ = download.Reader.Close() }()
	if download.Size < 0 {
		err := errors.New("Telegram download returned invalid file size")
		_ = p.mediaRepo.MarkFailed(ctx, payload.MediaID, err)
		return err
	}

	if err := p.media.StoreDownload(ctx, envelope.AccountID, payload.MediaID, download.Reader, download.Size, download.ContentType); err != nil {
		_ = p.mediaRepo.MarkFailed(ctx, payload.MediaID, err)
		return err
	}
	return p.writeAudit(ctx, envelope.AccountID, "TELEGRAM_MEDIA_STORED", "media", payload.MediaID, map[string]any{
		"message_id":       payload.MessageID,
		"telegram_file_id": payload.TelegramFileID,
		"size":             download.Size,
	})
}
