package media

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestImagePreviewBoundsAndContent(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2000, 1000))
	src.Set(0, 0, color.White)
	var b bytes.Buffer
	png.Encode(&b, src)
	out, err := ImagePreview(&b)
	if err != nil {
		t.Fatal(err)
	}
	c, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || format != "jpeg" || c.Width != 1280 || c.Height != 640 {
		t.Fatalf("preview: %+v %s %v", c, format, err)
	}
	if _, err := ImagePreview(bytes.NewBufferString("not an image")); err == nil {
		t.Fatal("invalid image accepted")
	}
}
func TestVideoFrameAndTimestamp(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	p := filepath.Join(t.TempDir(), "test.mp4")
	if err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=160x120:d=1", "-threads", "1", "-y", p).Run(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := VideoFrame(context.Background(), f, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(b)); err != nil {
		t.Fatal(err)
	}
	// Playlists could reference arbitrary local files; only media demuxers are allowed.
	playlist := bytes.NewBufferString("ffconcat version 1.0\nfile '" + p + "'\n")
	if _, err := VideoFrame(context.Background(), playlist, 0); err == nil {
		t.Fatal("local-file playlist accepted")
	}
	f.Seek(0, 0)
	if _, err := VideoFrame(context.Background(), f, 10); err == nil {
		t.Fatal("nonexistent timestamp accepted")
	}
}
