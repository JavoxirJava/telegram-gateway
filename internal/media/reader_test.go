package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
)

type memoryRepository struct {
	item Item
	gone bool
}

func (r *memoryRepository) GetActive(context.Context, string, string) (Item, error) {
	if r.gone {
		return Item{}, pgx.ErrNoRows
	}
	return r.item, nil
}
func (*memoryRepository) MarkFailed(context.Context, string, error) error { return nil }
func (*memoryRepository) MarkReady(context.Context, string, string, string, int64, []byte) error {
	return nil
}

type memoryObjectStore struct {
	data     []byte
	opens    int
	closed   bool
	declared int64
}

func (*memoryObjectStore) Put(context.Context, string, io.Reader, int64, string) (minio.UploadInfo, error) {
	return minio.UploadInfo{}, nil
}
func (s *memoryObjectStore) OpenReader(context.Context, string) (io.ReadCloser, int64, error) {
	s.opens++
	return &memoryReadCloser{Reader: bytes.NewReader(s.data), closed: &s.closed}, s.declared, nil
}

type memoryReadCloser struct {
	io.Reader
	closed *bool
}

func (r *memoryReadCloser) Close() error { *r.closed = true; return nil }
func readFixture(t *testing.T, n int) (*Service, *memoryRepository, *memoryObjectStore) {
	t.Helper()
	key, _ := ObjectKey("account", "media")
	size := int64(n)
	repo := &memoryRepository{item: Item{DownloadStatus: "ready", ObjectKey: &key, FileSize: &size}}
	store := &memoryObjectStore{data: bytes.Repeat([]byte{'a'}, n), declared: size}
	return NewService(repo, store), repo, store
}
func allowMedia(context.Context) error { return nil }
func TestOpenMediaFailsClosed(t *testing.T) {
	s, repo, store := readFixture(t, 10)
	repo.gone = true
	if _, err := s.Open(context.Background(), "account", "media", allowMedia); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	if store.opens != 0 {
		t.Fatal("deleted media reached storage")
	}
	repo.gone = false
	if _, err := s.Open(context.Background(), "account", "media", nil); err == nil {
		t.Fatal("nil authorizer accepted")
	}
	errDenied := errors.New("denied")
	if _, err := s.Open(context.Background(), "account", "media", func(context.Context) error { return errDenied }); !errors.Is(err, errDenied) {
		t.Fatal(err)
	}
	if store.opens != 0 {
		t.Fatal("unauthorized media reached storage")
	}
}
func TestMediaStopsOnDeleteAndBoundsReads(t *testing.T) {
	s, repo, store := readFixture(t, ReadChunkSize*3)
	content, err := s.Open(context.Background(), "account", "media", allowMedia)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, ReadChunkSize*2)
	n, err := content.Reader.Read(buf)
	if err != nil || n != ReadChunkSize {
		t.Fatalf("n=%d err=%v", n, err)
	}
	repo.gone = true
	n, err = content.Reader.Read(buf)
	if n != 0 || !errors.Is(err, ErrAccessChanged) {
		t.Fatalf("deleted content leaked: %d %v", n, err)
	}
	_ = content.Reader.Close()
	_ = content.Reader.Close()
	if !store.closed {
		t.Fatal("object reader not closed")
	}
}
func TestMediaStopsOnTokenRevocation(t *testing.T) {
	s, _, _ := readFixture(t, 10)
	denied := false
	authorize := func(context.Context) error {
		if denied {
			return ErrAccessChanged
		}
		return nil
	}
	content, err := s.Open(context.Background(), "account", "media", authorize)
	if err != nil {
		t.Fatal(err)
	}
	defer content.Reader.Close()
	denied = true
	if n, err := content.Reader.Read(make([]byte, 10)); n != 0 || !errors.Is(err, ErrAccessChanged) {
		t.Fatalf("revoked token read %d: %v", n, err)
	}
}
func TestMediaRejectsCrossAccountObjectKey(t *testing.T) {
	s, repo, store := readFixture(t, 10)
	key := "accounts/other/media/media"
	repo.item.ObjectKey = &key
	if _, err := s.Open(context.Background(), "account", "media", allowMedia); !errors.Is(err, ErrNotReady) {
		t.Fatal(err)
	}
	if store.opens != 0 {
		t.Fatal("cross-account object accessed")
	}
}
func TestMediaRejectsStorageSizeMismatch(t *testing.T) {
	s, _, store := readFixture(t, 10)
	store.declared = 11
	if _, err := s.Open(context.Background(), "account", "media", allowMedia); err == nil {
		t.Fatal("mismatched object accepted")
	}
	if !store.closed {
		t.Fatal("mismatched object leaked handle")
	}
}
func TestMediaReadsOnlyDeclaredBytes(t *testing.T) {
	s, _, store := readFixture(t, 10)
	store.data = append(store.data, []byte("must not leak")...)
	content, err := s.Open(context.Background(), "account", "media", allowMedia)
	if err != nil {
		t.Fatal(err)
	}
	defer content.Reader.Close()
	data, err := io.ReadAll(content.Reader)
	if err != nil || len(data) != 10 {
		t.Fatalf("bytes=%d err=%v", len(data), err)
	}
}
func TestMediaDetectsTruncatedObject(t *testing.T) {
	s, _, store := readFixture(t, 10)
	store.data = store.data[:5]
	content, err := s.Open(context.Background(), "account", "media", allowMedia)
	if err != nil {
		t.Fatal(err)
	}
	defer content.Reader.Close()
	_, err = io.ReadAll(content.Reader)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
}
