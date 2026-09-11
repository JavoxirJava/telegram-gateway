package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/syncjob"
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
	if item.TelegramFileID == nil || *item.TelegramFileID != payload.TelegramFileID {
		return Permanent(errors.New("media job Telegram file id does not match stored media"))
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

	session, err := p.sessions.Get(ctx, envelope.AccountID)
	if err != nil {
		_ = p.mediaRepo.MarkFailed(ctx, payload.MediaID, err)
		return err
	}
	download, err := session.DownloadFile(ctx, payload.TelegramFileID)
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
