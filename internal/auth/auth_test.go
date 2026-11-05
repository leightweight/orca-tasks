package auth

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/argon2id"
)

func TestPassword(t *testing.T) {
	t.Parallel()

	if _, err := HashPassword("short"); err == nil {
		t.Error("a short password was accepted")
	}

	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}

	if !CheckPassword("correct horse battery", hash) {
		t.Error("the right password didn't match")
	}

	if CheckPassword("wrong horse battery", hash) {
		t.Error("the wrong password matched")
	}
}

func TestAPIKey(t *testing.T) {
	t.Parallel()

	key, hash := NewAPIKey()
	if !strings.HasPrefix(key, APIKeyPrefix) {
		t.Errorf("key %q lacks the prefix", key)
	}

	if !bytes.Equal(HashToken(key), hash) {
		t.Error("hashing the key again gave a different hash")
	}

	other, _ := NewAPIKey()
	if other == key {
		t.Error("two keys were the same")
	}
}

func TestRecoveryCodes(t *testing.T) {
	t.Parallel()

	codes, hashes := NewRecoveryCodes()
	if len(codes) != RecoveryCodeCount || len(hashes) != RecoveryCodeCount {
		t.Fatalf("got %d codes and %d hashes", len(codes), len(hashes))
	}

	typed := strings.ToUpper(strings.ReplaceAll(codes[0], "-", " "))
	if !bytes.Equal(HashRecoveryCode(typed), hashes[0]) {
		t.Errorf("%q as typed %q didn't match its hash", codes[0], typed)
	}
}

func TestSecretBox(t *testing.T) {
	t.Parallel()

	box, err := ParseSecretKey("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}

	sealed := box.Seal([]byte("JBSWY3DPEHPK3PXP"))

	plain, err := box.Open(sealed)
	if err != nil || string(plain) != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("Open = %q, %v", plain, err)
	}

	other, err := ParseSecretKey("ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA=")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := other.Open(sealed); err == nil {
		t.Error("another key opened the value")
	}

	if _, err := ParseSecretKey("dG9vIHNob3J0"); err == nil {
		t.Error("a short key was accepted")
	}
}

func TestTOTP(t *testing.T) {
	t.Parallel()

	enrollment, err := NewEnrollment("orca-tasks", "alice")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(enrollment.QRCode, "data:image/png;base64,") {
		t.Errorf("QR code is %.30q", enrollment.QRCode)
	}

	now := time.Date(2026, 11, 5, 12, 0, 10, 0, time.UTC)
	code := TOTPCode(enrollment.Secret, now)

	step, ok := CheckTOTP(enrollment.Secret, code, now, 0)
	if !ok {
		t.Fatal("the current code was rejected")
	}

	if _, ok := CheckTOTP(enrollment.Secret, code, now, step); ok {
		t.Error("the same code was accepted twice")
	}

	if _, ok := CheckTOTP(enrollment.Secret, TOTPCode(enrollment.Secret, now.Add(-30*time.Second)), now, 0); !ok {
		t.Error("the previous step's code was rejected")
	}

	if _, ok := CheckTOTP(enrollment.Secret, TOTPCode(enrollment.Secret, now.Add(-90*time.Second)), now, 0); ok {
		t.Error("a code from three steps ago was accepted")
	}

	if _, ok := CheckTOTP(enrollment.Secret, "12345", now, 0); ok {
		t.Error("a five-digit code was accepted")
	}
}

func TestPasswordParameters(t *testing.T) {
	t.Parallel()

	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(hash, "m=19456,t=2,p=1") {
		t.Errorf("hash %q doesn't use the capped parameters", hash)
	}

	// A hash made with the library's defaults, as older servers did, still
	// verifies.
	old, err := argon2id.CreateHash("correct horse battery", argon2id.DefaultParams)
	if err != nil {
		t.Fatal(err)
	}

	if !CheckPassword("correct horse battery", old) {
		t.Error("a hash with the old parameters no longer verifies")
	}
}
