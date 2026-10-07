package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const MaxPreviewBytes int64 = 100 << 20

// ImagePreview bounds both decoded pixels and the returned image size.
func ImagePreview(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, 16<<20+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 16<<20 {
		return nil, errors.New("image exceeds 16 MiB")
	}
	c, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("unsupported image; use JPEG, PNG or GIF")
	}
	if c.Width < 1 || c.Height < 1 || int64(c.Width)*int64(c.Height) > 40_000_000 {
		return nil, errors.New("image exceeds 40 megapixels")
	}
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	w, h := c.Width, c.Height
	if w > 1280 || h > 1280 {
		if w >= h {
			h = max(1, h*1280/w)
			w = 1280
		} else {
			w = max(1, w*1280/h)
			h = 1280
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	bounds := src.Bounds()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(x, y, src.At(bounds.Min.X+x*c.Width/w, bounds.Min.Y+y*c.Height/h))
		}
	}
	var out bytes.Buffer
	err = jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85})
	return out.Bytes(), err
}

// VideoFrame samples a requested timestamp; it is not an audio transcript or a
// claim that the full video was examined. Inputs and temporary output are bounded.
func VideoFrame(ctx context.Context, r io.Reader, second float64) ([]byte, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, errors.New("video preview requires ffmpeg on the gateway")
	}
	dir, err := os.MkdirTemp("", "tgw-preview-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "input")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(f, io.LimitReader(r, MaxPreviewBytes+1))
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if n > MaxPreviewBytes {
		return nil, errors.New("video exceeds 100 MiB preview limit")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	output := filepath.Join(dir, "frame.jpg")
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-threads", "1", "-filter_threads", "1", "-max_alloc", "67108864", "-protocol_whitelist", "file,pipe", "-format_whitelist", "mov,matroska,webm,avi,mpeg,mpegts,ogg,gif", "-ss", strconv.FormatFloat(second, 'f', 3, 64), "-i", path, "-map", "0:v:0", "-frames:v", "1", "-vf", "scale=1280:1280:force_original_aspect_ratio=decrease", "-q:v", "3", "-fs", "4194304", output)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("video frame unavailable at %.3fs", second)
	}
	f, err = os.Open(output)
	if err != nil {
		return nil, errors.New("no frame at this timestamp")
	}
	defer f.Close()
	return ImagePreview(f)
}
