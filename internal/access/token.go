package access

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

const (
	tokenPrefix     = "tgw_"
	randomTokenSize = 32
	visiblePrefix   = 16
)

type GeneratedToken struct {
	Plaintext string
	Prefix    string
	Hash      []byte
}

func GenerateToken() (GeneratedToken, error) {
	random := make([]byte, randomTokenSize)
	if _, err := rand.Read(random); err != nil {
		return GeneratedToken{}, err
	}

	plaintext := tokenPrefix + base64.RawURLEncoding.EncodeToString(random)
	return TokenFromPlaintext(plaintext)
}

func TokenFromPlaintext(plaintext string) (GeneratedToken, error) {
	plaintext = strings.TrimSpace(plaintext)
	if plaintext == "" {
		return GeneratedToken{}, errors.New("token cannot be empty")
	}
	if !strings.HasPrefix(plaintext, tokenPrefix) {
		return GeneratedToken{}, errors.New("invalid token prefix")
	}

	digest := sha256.Sum256([]byte(plaintext))
	prefixLength := visiblePrefix
	if len(plaintext) < prefixLength {
		prefixLength = len(plaintext)
	}

	return GeneratedToken{
		Plaintext: plaintext,
		Prefix:    plaintext[:prefixLength],
		Hash:      digest[:],
	}, nil
}
