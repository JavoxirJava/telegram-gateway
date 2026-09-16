package media

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/objectstore"
)

const readURLTTL = 5 * time.Minute

var ErrNotReady = errors.New("media is not ready")

type ReadURL struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Service struct {
	repository *Repository
	store      *objectstore.Store
}

func NewService(repository *Repository, store *objectstore.Store) *Service {
	return &Service{repository: repository, store: store}
}

func (s *Service) StoreDownload(ctx context.Context, accountID, mediaID string, reader io.Reader, size int64, contentType string) error {
	if reader == nil {
		return errors.New("media reader is required")
	}
	if size < 0 {
		return errors.New("media size cannot be negative")
	}
	objectKey, err := ObjectKey(accountID, mediaID)
	if err != nil {
		return err
	}

	hasher := sha256.New()
	stream := io.TeeReader(reader, hasher)
	if _, err := s.store.Put(ctx, objectKey, stream, size, strings.TrimSpace(contentType)); err != nil {
		_ = s.repository.MarkFailed(ctx, mediaID, err)
		return fmt.Errorf("store downloaded media: %w", err)
	}
	if err := s.repository.MarkReady(ctx, mediaID, objectKey, strings.TrimSpace(contentType), size, hasher.Sum(nil)); err != nil {
		return fmt.Errorf("persist downloaded media state: %w", err)
	}
	return nil
}

func (s *Service) CreateReadURL(ctx context.Context, accountID, mediaID string) (ReadURL, error) {
	item, err := s.repository.GetActive(ctx, accountID, mediaID)
	if err != nil {
		return ReadURL{}, err
	}
	if item.DownloadStatus != "ready" || item.ObjectKey == nil || strings.TrimSpace(*item.ObjectKey) == "" {
		return ReadURL{}, ErrNotReady
	}

	fileName := ""
	if item.FileName != nil {
		fileName = *item.FileName
	}
	presigned, err := s.store.PresignedGet(ctx, *item.ObjectKey, readURLTTL, fileName)
	if err != nil {
		return ReadURL{}, fmt.Errorf("create media read url: %w", err)
	}
	return ReadURL{
		URL:       presigned.String(),
		ExpiresAt: time.Now().UTC().Add(readURLTTL),
	}, nil
}

func ObjectKey(accountID, mediaID string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	mediaID = strings.TrimSpace(mediaID)
	if accountID == "" || mediaID == "" {
		return "", errors.New("account id and media id are required")
	}
	if strings.ContainsAny(accountID, "/\\") || strings.ContainsAny(mediaID, "/\\") {
		return "", errors.New("invalid object key identifier")
	}
	return "accounts/" + accountID + "/media/" + mediaID, nil
}

func (s *Service) OpenRead(ctx context.Context, accountID, mediaID string) (io.ReadCloser, int64, string, string, error) {
	item, err := s.repository.GetActive(ctx, accountID, mediaID)
	if err != nil {
		return nil, 0, "", "", err
	}
	if item.DownloadStatus != "ready" || item.ObjectKey == nil {
		return nil, 0, "", "", ErrNotReady
	}
	obj, err := s.store.Get(ctx, *item.ObjectKey)
	if err != nil {
		return nil, 0, "", "", err
	}
	stat, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, 0, "", "", err
	}
	name := "download"
	if item.FileName != nil {
		name = *item.FileName
	}
	return obj, stat.Size, stat.ContentType, name, nil
}
func (s *Service) CheckReady(ctx context.Context, accountID, mediaID string) error {
	item, err := s.repository.GetActive(ctx, accountID, mediaID)
	if err != nil {
		return err
	}
	if item.DownloadStatus != "ready" || item.ObjectKey == nil {
		return ErrNotReady
	}
	return nil
}
