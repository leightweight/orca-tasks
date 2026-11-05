// Package auth holds the credential handling that doesn't touch the database:
// password hashes, API keys and other tokens, TOTP, and the encryption that
// protects TOTP secrets at rest.
package auth

import (
	"errors"
	"unicode/utf8"

	"github.com/alexedwards/argon2id"
)

// MinPasswordLength is the shortest password accepted.
const MinPasswordLength = 12

// ErrPasswordTooShort is returned for a password under MinPasswordLength.
var ErrPasswordTooShort = errors.New("passwords must be at least 12 characters")

// HashPassword hashes a password with Argon2id.
func HashPassword(password string) (string, error) {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return "", ErrPasswordTooShort
	}

	return argon2id.CreateHash(password, argon2id.DefaultParams)
}

// CheckPassword reports whether a password matches a hash from HashPassword.
func CheckPassword(password, hash string) bool {
	match, err := argon2id.ComparePasswordAndHash(password, hash)
	return err == nil && match
}
