package media

import (
	"context"
	"errors"
	"fmt"
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
