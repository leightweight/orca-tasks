package auth

import (
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/nacl/secretbox"
)

// SecretBox encrypts values that must be read back, such as TOTP secrets.
type SecretBox struct {
	key [32]byte
}

// ParseSecretKey reads 32 bytes of base64, standard or URL-safe.
func ParseSecretKey(text string) (*SecretBox, error) {
	var raw []byte

	var err error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err = enc.DecodeString(text); err == nil {
			break
		}
	}

	if err != nil {
		return nil, fmt.Errorf("the secret key is not base64: %w", err)
	}

	if len(raw) != 32 {
		return nil, fmt.Errorf("the secret key is %d bytes, and must be 32", len(raw))
	}

	box := &SecretBox{}
	copy(box.key[:], raw)

	return box, nil
}

// Seal encrypts a value, with a random nonce in front.
func (b *SecretBox) Seal(plain []byte) []byte {
	var nonce [24]byte
	copy(nonce[:], randomBytes(24))

	return secretbox.Seal(nonce[:], plain, &nonce, &b.key)
}

// ErrUnreadable means a sealed value was made with another key, or altered.
var ErrUnreadable = errors.New("the value can't be decrypted with this secret key")

// Open decrypts a value from Seal.
func (b *SecretBox) Open(sealed []byte) ([]byte, error) {
	if len(sealed) < 24 {
		return nil, ErrUnreadable
	}

	var nonce [24]byte
	copy(nonce[:], sealed[:24])

	plain, ok := secretbox.Open(nil, sealed[24:], &nonce, &b.key)
	if !ok {
		return nil, ErrUnreadable
	}

	return plain, nil
}
