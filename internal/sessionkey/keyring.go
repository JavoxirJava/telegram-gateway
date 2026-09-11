// Package sessionkey encrypts per-account TDLib database keys. It does not
// encrypt PostgreSQL message bodies or media files; those need encrypted
// volumes / object-store encryption configured separately.
package sessionkey

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
)

var accountPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func ValidAccountID(id string) bool { return accountPattern.MatchString(id) }

type Keyring struct {
	active string
	keys   map[string][]byte
}
type envelope struct {
	Version    int    `json:"version"`
	KeyID      string `json:"key_id"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

func New(active string, keys map[string][]byte) (*Keyring, error) {
	if !keyIDPattern.MatchString(active) {
		return nil, errors.New("invalid active encryption key id")
	}
	result := &Keyring{active: active, keys: make(map[string][]byte, len(keys))}
	for id, key := range keys {
		if !keyIDPattern.MatchString(id) || len(key) != 32 {
			return nil, errors.New("master keys require valid ids and exactly 32 bytes")
		}
		result.keys[id] = append([]byte(nil), key...)
	}
	if _, ok := result.keys[active]; !ok {
		return nil, errors.New("active master key is missing")
	}
	return result, nil
}

// LoadMasterFile accepts a base64-encoded 32-byte key in an owner-only regular
// file. Do not supply keys in argv, API inputs, audit metadata or logs.
func LoadMasterFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("cannot inspect master key file")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("master key must be an owner-only regular file (0600)")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open master key file")
	}
	defer f.Close()
	current, err := f.Stat()
	if err != nil || !os.SameFile(info, current) {
		return nil, errors.New("master key file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return nil, errors.New("invalid master key file")
	}
	defer clear(data)
	decoded, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(decoded) != 32 {
		clear(decoded)
		return nil, errors.New("master key file must contain base64 for exactly 32 bytes")
	}
	return decoded, nil
}
func aad(accountID, keyID string) []byte {
	return []byte("telegram-gateway/tdlib-key/v1/" + accountID + "/" + keyID)
}
func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (k *Keyring) Seal(accountID string, key []byte) ([]byte, error) {
	if !ValidAccountID(accountID) || len(key) != 32 {
		return nil, errors.New("invalid account or TDLib key")
	}
	a, err := gcm(k.keys[k.active])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	e := envelope{Version: 1, KeyID: k.active, Nonce: nonce, Ciphertext: a.Seal(nil, nonce, key, aad(accountID, k.active))}
	return json.Marshal(e)
}
func (k *Keyring) Open(accountID string, data []byte) ([]byte, error) {
	if !ValidAccountID(accountID) || len(data) > 4096 {
		return nil, errors.New("invalid encrypted session key")
	}
	var e envelope
	if json.Unmarshal(data, &e) != nil || e.Version != 1 {
		return nil, errors.New("unsupported encrypted session key format")
	}
	master, ok := k.keys[e.KeyID]
	if !ok {
		return nil, errors.New("required master key is unavailable")
	}
	a, err := gcm(master)
	if err != nil {
		return nil, err
	}
	if len(e.Nonce) != a.NonceSize() || len(e.Ciphertext) != 32+a.Overhead() {
		return nil, errors.New("invalid encrypted session key length")
	}
	key, err := a.Open(nil, e.Nonce, e.Ciphertext, aad(accountID, e.KeyID))
	if err != nil {
		return nil, errors.New("session key authentication failed")
	}
	return key, nil
}
