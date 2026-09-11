//go:build tdlib && cgo && linux

package tdjson_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/tdjson"
)

func TestNativeJSONABI(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "libtdjson_fixture.so")
	output, err := exec.Command("cc", "-shared", "-fPIC", "-pthread", "testdata/tdjson_fixture.c", "-o", lib).CombinedOutput()
	if err != nil {
		t.Fatalf("compile ABI fixture: %v: %s", err, output)
	}
	transport, err := tdjson.OpenNative(lib)
	if err != nil {
		t.Fatal(err)
	}
	e, err := tdjson.New(transport)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := e.NewClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.Call(ctx, "getAuthorizationState", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "authorizationStateWaitTdlibParameters") {
		t.Fatal("wrong native response")
	}
	if err := e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tdjson.OpenNative(lib); err == nil {
		t.Fatal("second native receiver allowed")
	}
}
