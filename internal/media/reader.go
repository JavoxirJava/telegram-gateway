package media

import (
	"context"
	"errors"
	"io"
)

const ReadChunkSize = 64 * 1024

var ErrAccessChanged = errors.New("media access is no longer available")

// Content never contains a storage URL/key or a bearer credential.
type Content struct {
	Reader   io.ReadCloser
	Size     int64
	FileName string
}

// Open verifies authorization before opening storage and before every chunk.
// authorize must recheck the current token/grant; nil is deliberately rejected.
// Bytes already delivered, or authorized in a racing read, cannot be recalled.
func (s *Service) Open(ctx context.Context, accountID, mediaID string, authorize func(context.Context) error) (Content, error) {
	if authorize == nil {
		return Content{}, errors.New("media authorization callback is required")
	}
	if err := authorize(ctx); err != nil {
		return Content{}, err
	}
	item, err := s.repository.GetActive(ctx, accountID, mediaID)
	if err != nil {
		return Content{}, err
	}
	expected, err := ObjectKey(accountID, mediaID)
	if err != nil {
		return Content{}, err
	}
	if item.DownloadStatus != "ready" || item.ObjectKey == nil || *item.ObjectKey != expected || item.FileSize == nil || *item.FileSize < 0 {
		return Content{}, ErrNotReady
	}
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := authorize(ctx); err != nil {
			return err
		}
		current, err := s.repository.GetActive(ctx, accountID, mediaID)
		if err != nil {
			return ErrAccessChanged
		}
		if current.DownloadStatus != "ready" || current.ObjectKey == nil || *current.ObjectKey != expected || current.FileSize == nil || *current.FileSize != *item.FileSize {
			return ErrAccessChanged
		}
		return nil
	}
	reader, size, err := s.store.OpenReader(ctx, expected)
	if err != nil {
		return Content{}, err
	}
	if size != *item.FileSize {
		_ = reader.Close()
		return Content{}, errors.New("stored media size does not match metadata")
	}
	if err := check(); err != nil {
		_ = reader.Close()
		return Content{}, err
	}
	name := "download"
	if item.FileName != nil {
		name = *item.FileName
	}
	return Content{Reader: &checkedReader{source: reader, check: check, remaining: size}, Size: size, FileName: name}, nil
}

type checkedReader struct {
	source    io.ReadCloser
	check     func() error
	remaining int64
	closed    bool
}

// Readers are intentionally single-consumer, like the HTTP response body.
func (r *checkedReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if len(p) == 0 {
		return 0, nil
	}
	if err := r.check(); err != nil {
		return 0, err
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if len(p) > ReadChunkSize {
		p = p[:ReadChunkSize]
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.source.Read(p)
	r.remaining -= int64(n)
	if err == io.EOF && r.remaining > 0 {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}
func (r *checkedReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	return r.source.Close()
}
