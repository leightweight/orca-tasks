package local

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckVersionRefusesOtherVersions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := filepath.Join(dir, "postgres")

	if err := checkVersion(data); err != nil {
		t.Errorf("a missing directory: %v", err)
	}

	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := checkVersion(data); err != nil {
		t.Errorf("an empty directory: %v", err)
	}

	if err := os.WriteFile(filepath.Join(data, "PG_VERSION"), []byte("17\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := checkVersion(data); !errors.Is(err, ErrOtherVersion) {
		t.Errorf("a Postgres 17 directory: %v, want ErrOtherVersion", err)
	}

	if err := os.WriteFile(filepath.Join(data, "PG_VERSION"), []byte(postgresMajorVersion+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := checkVersion(data); err != nil {
		t.Errorf("a matching directory: %v", err)
	}
}

func TestSecretsAreMadeOnceAndPrivate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	first, err := SecretKey(dir)
	if err != nil {
		t.Fatal(err)
	}

	second, err := SecretKey(dir)
	if err != nil || second != first {
		t.Errorf("the second read gave a different key: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "secret-key"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("secret-key mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
}
