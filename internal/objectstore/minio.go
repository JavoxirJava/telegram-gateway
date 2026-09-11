package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/JavoxirJava/telegram-gateway/internal/config"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Store struct {
	client *minio.Client
	bucket string
}

func Open(ctx context.Context, cfg config.MinIOConfig) (*Store, error) {
	if strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		return nil, errors.New("MinIO credentials are required")
	}

	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("create MinIO client: %w", err)
	}

	store := &Store{client: client, bucket: cfg.Bucket}
	if err := store.ensureBucket(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Put(ctx context.Context, objectKey string, reader io.Reader, size int64, contentType string) (minio.UploadInfo, error) {
	if strings.TrimSpace(objectKey) == "" {
		return minio.UploadInfo{}, errors.New("object key is required")
	}

	info, err := s.client.PutObject(ctx, s.bucket, objectKey, reader, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return minio.UploadInfo{}, fmt.Errorf("put MinIO object: %w", err)
	}
	return info, nil
}

func (s *Store) Get(ctx context.Context, objectKey string) (*minio.Object, error) {
	if strings.TrimSpace(objectKey) == "" {
		return nil, errors.New("object key is required")
	}
	return s.client.GetObject(ctx, s.bucket, objectKey, minio.GetObjectOptions{})
}

func (s *Store) Stat(ctx context.Context, objectKey string) (minio.ObjectInfo, error) {
	if strings.TrimSpace(objectKey) == "" {
		return minio.ObjectInfo{}, errors.New("object key is required")
	}
	info, err := s.client.StatObject(ctx, s.bucket, objectKey, minio.StatObjectOptions{})
	if err != nil {
		return minio.ObjectInfo{}, fmt.Errorf("stat MinIO object: %w", err)
	}
	return info, nil
}

func (s *Store) ensureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check MinIO bucket: %w", err)
	}
	if exists {
		return nil
	}
	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
		return fmt.Errorf("create MinIO bucket: %w", err)
	}
	return nil
}
