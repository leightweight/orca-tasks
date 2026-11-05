package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

// APIKeyPrefix starts every API key, so a leaked key is easy to recognize.
const APIKeyPrefix = "otk_"

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewToken returns a random token of 32 bytes in lowercase base32, with a
// prefix, and the hash to store for it.
func NewToken(prefix string) (token string, hash []byte) {
	token = prefix + strings.ToLower(encoding.EncodeToString(randomBytes(32)))
	return token, HashToken(token)
}

// NewAPIKey returns a new API key and the hash to store for it.
func NewAPIKey() (key string, hash []byte) {
	return NewToken(APIKeyPrefix)
}

// HashToken hashes a token for storage or lookup. The tokens are 256 random
// bits, so a fast hash is enough: nobody can guess one to match a stolen hash.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// RecoveryCodeCount is how many recovery codes a user gets at a time.
const RecoveryCodeCount = 10

// NewRecoveryCodes returns a set of single-use recovery codes, such as
// "k4vqd-3m7xa", and their hashes.
func NewRecoveryCodes() (codes []string, hashes [][]byte) {
	for range RecoveryCodeCount {
		raw := strings.ToLower(encoding.EncodeToString(randomBytes(7)))[:10]
		code := raw[:5] + "-" + raw[5:]
		codes = append(codes, code)
		hashes = append(hashes, HashRecoveryCode(code))
	}

	return codes, hashes
}

// HashRecoveryCode hashes a recovery code as typed, ignoring case, spaces, and
// the hyphen.
func HashRecoveryCode(code string) []byte {
	code = strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(code))
	return HashToken("recovery:" + code)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	// crypto/rand.Read never returns an error.
	_, _ = rand.Read(b)

	return b
}
