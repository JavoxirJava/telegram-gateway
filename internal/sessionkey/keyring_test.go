package sessionkey

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

const accountA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
const accountB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

func testRing(t *testing.T) *Keyring {
	t.Helper()
	k, err := New("primary", map[string][]byte{"primary": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return k
}
func TestEnvelopeRoundTripAndTenantBinding(t *testing.T) {
	k := testRing(t)
	key := bytes.Repeat([]byte{9}, 32)
	one, err := k.Seal(accountA, key)
	if err != nil {
		t.Fatal(err)
	}
	two, _ := k.Seal(accountA, key)
	if bytes.Equal(one, two) {
		t.Fatal("nonce reused")
	}
	decoded, err := k.Open(accountA, one)
	if err != nil || !bytes.Equal(decoded, key) {
		t.Fatal(err)
	}
	if _, err := k.Open(accountB, one); err == nil {
		t.Fatal("cross-account ciphertext accepted")
	}
	one[len(one)/2] ^= 1
	if _, err := k.Open(accountA, one); err == nil {
		t.Fatal("tampering accepted")
	}
}
func TestRejectUnknownOrShortKeys(t *testing.T) {
	if _, err := New("one", map[string][]byte{"one": {1}}); err == nil {
		t.Fatal("short master key accepted")
	}
	k := testRing(t)
	data, _ := k.Seal(accountA, make([]byte, 32))
	other, _ := New("other", map[string][]byte{"other": make([]byte, 32)})
	if _, err := other.Open(accountA, data); err == nil {
		t.Fatal("unknown master key accepted")
	}
}
func TestLoadMasterPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master")
	text := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMasterFile(path); err == nil {
		t.Fatal("world-readable master accepted")
	}
	_ = os.Chmod(path, 0600)
	if _, err := LoadMasterFile(path); err != nil {
		t.Fatal(err)
	}
}
