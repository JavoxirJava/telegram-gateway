//go:build linux

package sessionkey

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceLocksAndReusesKey(t *testing.T) {
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	k := testRing(t)
	w, err := OpenWorkspace(root, accountA, k)
	if err != nil {
		t.Fatal(err)
	}
	saved := append([]byte(nil), w.DatabaseKey...)
	if _, err := OpenWorkspace(root, accountA, k); err == nil {
		t.Fatal("two owners opened session")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(w.DatabaseKey, make([]byte, 32)) {
		t.Fatal("key not cleared on close")
	}
	reopened, err := OpenWorkspace(root, accountA, k)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !bytes.Equal(saved, reopened.DatabaseKey) {
		t.Fatal("database key changed on restart")
	}
}
func TestWorkspaceRejectsSymlinksAndTraversal(t *testing.T) {
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	k := testRing(t)
	for _, id := range []string{"../escape", "", accountA + "/x"} {
		if _, err := OpenWorkspace(root, id, k); err == nil {
			t.Fatal("invalid account accepted")
		}
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, accountA)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWorkspace(root, accountA, k); err == nil {
		t.Fatal("symlink accepted")
	}
}
