package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"strconv"
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

// HashToken hashes a token for storage or lookup. API keys and reset links are
// 256 random bits, so a fast hash is enough: nobody can guess one to match a
// stolen hash.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// RecoveryCodeCount is how many recovery codes a user gets at a time.
const RecoveryCodeCount = 10

// NewRecoveryCodes returns a set of single-use recovery codes, such as
// "k4vq-d3m7-xa2b-9fhw": 80 random bits each.
func NewRecoveryCodes() []string {
	codes := make([]string, 0, RecoveryCodeCount)

	for range RecoveryCodeCount {
		raw := strings.ToLower(encoding.EncodeToString(randomBytes(10)))
		codes = append(codes, raw[0:4]+"-"+raw[4:8]+"-"+raw[8:12]+"-"+raw[12:16])
	}

	return codes
}

// HashRecoveryCodes hashes a user's new recovery codes for storage.
func HashRecoveryCodes(principalID int64, codes []string) [][]byte {
	hashes := make([][]byte, len(codes))
	for i, code := range codes {
		hashes[i] = HashRecoveryCode(principalID, code)
	}

	return hashes
}

// legacyRecoveryCodeLength is the length of the codes older servers made, which
// were hashed without the principal.
const legacyRecoveryCodeLength = 10

// HashRecoveryCode hashes a recovery code as typed, ignoring case, spaces, and
// hyphens. The hash includes the principal, so one pass over a database dump
// can't crack every user's codes at once. An 80-bit code needs no slow hash:
// guessing one takes 2^80 tries for each user.
func HashRecoveryCode(principalID int64, code string) []byte {
	code = strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(code))

	if len(code) == legacyRecoveryCodeLength {
		return HashToken("recovery:" + code)
	}

	return HashToken("recovery:" + strconv.FormatInt(principalID, 10) + ":" + code)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	// crypto/rand.Read never returns an error.
	_, _ = rand.Read(b)

	return b
}
