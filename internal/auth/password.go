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

// passwordParams are OWASP's Argon2id recommendation: 19 MiB, two passes, one
// thread. The library's default uses 64 MiB a hash, so a few sign-ins at once
// could exhaust a small pod. A hash records its parameters, so hashes made
// with other parameters still verify.
var passwordParams = &argon2id.Params{
	Memory:      19 * 1024,
	Iterations:  2,
	Parallelism: 1,
	SaltLength:  16,
	KeyLength:   32,
}

// hashing limits how many hashes run at once, which caps their memory at
// twice one hash's whatever the number of sign-ins.
var hashing = make(chan struct{}, 2)

// HashPassword hashes a password with Argon2id.
func HashPassword(password string) (string, error) {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return "", ErrPasswordTooShort
	}

	hashing <- struct{}{}
	defer func() { <-hashing }()

	return argon2id.CreateHash(password, passwordParams)
}

// CheckPassword reports whether a password matches a hash from HashPassword.
func CheckPassword(password, hash string) bool {
	hashing <- struct{}{}
	defer func() { <-hashing }()

	match, err := argon2id.ComparePasswordAndHash(password, hash)

	return err == nil && match
}
