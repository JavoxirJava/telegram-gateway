package media

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
)

var ErrNotReady = errors.New("media is not ready")

// RepositoryStore is the storage boundary used by both ingestion and reads.
// Implementations must resolve reads through the active-message projection.
type RepositoryStore interface {
	GetActive(context.Context, string, string) (Item, error)
	MarkFailed(context.Context, string, error) error
	MarkReady(context.Context, string, string, string, int64, []byte) error
}

type ObjectStore interface {
	Put(context.Context, string, io.Reader, int64, string) (minio.UploadInfo, error)
	OpenReader(context.Context, string) (io.ReadCloser, int64, error)
}

type Service struct {
	repository RepositoryStore
	store      ObjectStore
}

func NewService(repository RepositoryStore, store ObjectStore) *Service {
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
