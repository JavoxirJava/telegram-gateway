package tdadapter

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
)

var ErrFileSize = errors.New("Telegram file exceeds the configured size or has inconsistent length")
var ErrFilePath = errors.New("Telegram file is outside the private files directory or is not regular")

// DownloadFile is worker-only. Resolve file IDs from an authorized active media
// record, never arbitrary client input. TDLib file IDs must be refreshed after
// session recreation; durable ID re-resolution belongs to account routing.
func (a *Adapter) DownloadFile(ctx context.Context, id int64) (telegram.Download, error) {
	if id <= 0 || id > 2147483647 {
		return telegram.Download{}, errors.New("invalid Telegram file id")
	}
	var before fileWire
	if err := a.read(ctx, "getFile", map[string]any{"file_id": id}, "file", &before); err != nil {
		return telegram.Download{}, err
	}
	if before.ID != id {
		return telegram.Download{}, ErrInvalidResponse
	}
	if before.Size < 0 || before.ExpectedSize < 0 || before.Size > a.cfg.MaxFileBytes || before.ExpectedSize > a.cfg.MaxFileBytes {
		return telegram.Download{}, ErrFileSize
	}
	var f fileWire
	// Even unknown-size transfers are capped at the native layer. A timeout may
	// leave native work in flight, but it cannot initiate an unlimited transfer.
	err := a.read(ctx, "downloadFile", map[string]any{"file_id": id, "priority": 1, "offset": int64(0), "limit": a.cfg.MaxFileBytes + 1, "synchronous": true}, "file", &f)
	if err != nil {
		return telegram.Download{}, err
	}
	if f.ID != id {
		return telegram.Download{}, ErrInvalidResponse
	}
	if f.Size < 0 || f.ExpectedSize < 0 || f.Size > a.cfg.MaxFileBytes || f.ExpectedSize > a.cfg.MaxFileBytes {
		return telegram.Download{}, ErrFileSize
	}
	if !f.Local.Complete {
		return telegram.Download{}, &Pending{}
	}
	if !filepath.IsAbs(f.Local.Path) {
		return telegram.Download{}, ErrFilePath
	}
	rel, err := filepath.Rel(a.cfg.FilesDirectory, f.Local.Path)
	if err != nil || !filepath.IsLocal(rel) {
		return telegram.Download{}, ErrFilePath
	}
	// os.Root prevents both path traversal and symlink escapes, including races;
	// string-prefix checks followed by os.Open would not be sufficient.
	// NONBLOCK prevents a substituted FIFO/device from hanging before Stat.
	file, err := a.files.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return telegram.Download{}, ErrFilePath
	}
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		file.Close()
		return telegram.Download{}, ErrFilePath
	}
	if stat.Size() != f.Size || stat.Size() > a.cfg.MaxFileBytes {
		file.Close()
		return telegram.Download{}, ErrFileSize
	}
	reader := &downloadReader{file: file, ctx: ctx, authorize: a.cfg.Authorize, left: stat.Size()}
	return telegram.Download{Reader: reader, Size: stat.Size(), ContentType: "application/octet-stream"}, nil
}

type downloadReader struct {
	file      io.ReadCloser
	ctx       context.Context
	authorize func(context.Context) error
	left      int64
}

func (r *downloadReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if err := r.authorize(r.ctx); err != nil {
		return 0, err
	}
	if r.left == 0 {
		return 0, io.EOF
	}
	if len(p) > 64<<10 {
		p = p[:64<<10]
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	n, err := r.file.Read(p)
	r.left -= int64(n)
	if err == io.EOF && r.left > 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
func (r *downloadReader) Close() error { return r.file.Close() }
