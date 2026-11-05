// Package local runs orca-tasks on one person's machine: an embedded Postgres,
// attachments, and a secret key in one directory, and a socket that lets the
// person's own agents use the API without an API key.
package local

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

// PostgresVersion is the Postgres the local database runs. Changing it needs
// a migration of existing data directories; see Start.
const (
	PostgresVersion      = embeddedpostgres.V18
	postgresMajorVersion = "18"
)

// Dir is the local mode directory: $ORCA_TASKS_LOCAL_DIR, or orca-tasks under
// $XDG_DATA_HOME, which defaults to ~/.local/share.
func Dir() (string, error) {
	if dir := os.Getenv("ORCA_TASKS_LOCAL_DIR"); dir != "" {
		return dir, nil
	}

	if data := os.Getenv("XDG_DATA_HOME"); data != "" {
		return filepath.Join(data, "orca-tasks"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".local", "share", "orca-tasks"), nil
}

// SocketPath is where the agent socket lives in a local mode directory.
func SocketPath(dir string) string { return filepath.Join(dir, "agent.sock") }

// AttachmentsDir is where attachments live in a local mode directory.
func AttachmentsDir(dir string) string { return filepath.Join(dir, "attachments") }

// SecretKey reads the directory's secret key, creating one the first time.
func SecretKey(dir string) (string, error) {
	path := filepath.Join(dir, "secret-key")

	if data, err := os.ReadFile(filepath.Clean(path)); err == nil {
		return strings.TrimSpace(string(data)), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	key := make([]byte, 32)
	_, _ = rand.Read(key)
	encoded := base64.StdEncoding.EncodeToString(key)

	return encoded, writeNew(path, encoded+"\n")
}

// Prepare creates a local mode directory, readable only by its owner.
func Prepare(dir string) error {
	return os.MkdirAll(dir, 0o700)
}

// Postgres is the embedded database.
type Postgres struct {
	URL string
	db  *embeddedpostgres.EmbeddedPostgres
}

// ErrOtherVersion means the data directory belongs to another Postgres major
// version, which this build won't open.
var ErrOtherVersion = errors.New("the database was created by another Postgres version")

// StartPostgres starts Postgres on a free port on 127.0.0.1, with its data in
// dir/postgres. It downloads the Postgres binaries the first time.
//
// embedded-postgres deletes a data directory whose PG_VERSION doesn't match
// the version it runs and starts over, so this checks first and refuses,
// rather than lose someone's tasks to a version bump.
func StartPostgres(ctx context.Context, dir string, log *slog.Logger) (*Postgres, error) {
	data := filepath.Join(dir, "postgres")

	if err := checkVersion(data); err != nil {
		return nil, err
	}

	stopOrphan(ctx, dir, data, log)

	password, err := postgresPassword(dir)
	if err != nil {
		return nil, err
	}

	port, err := freePort(ctx)
	if err != nil {
		return nil, err
	}

	db := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(PostgresVersion).
		Port(port).
		Username("orca").
		Password(password).
		Database("orca").
		DataPath(data).
		// The runtime directory is emptied on every start, so it must not
		// hold anything else.
		RuntimePath(filepath.Join(dir, "postgres-runtime")).
		BinariesPath(filepath.Join(dir, "postgres-bin")).
		CachePath(filepath.Join(dir, "postgres-cache")).
		StartTimeout(time.Minute).
		Logger(&logWriter{log: log}))

	if err := db.Start(); err != nil {
		return nil, fmt.Errorf("starting Postgres: %w", err)
	}

	return &Postgres{
		URL: fmt.Sprintf("postgres://orca:%s@127.0.0.1:%d/orca?sslmode=disable", password, port),
		db:  db,
	}, nil
}

// Stop stops Postgres.
func (p *Postgres) Stop() error { return p.db.Stop() }

func checkVersion(data string) error {
	entries, err := os.ReadDir(data)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(entries) == 0) {
		return nil
	}

	if err != nil {
		return err
	}

	version, err := os.ReadFile(filepath.Clean(filepath.Join(data, "PG_VERSION")))
	if err != nil {
		return fmt.Errorf("%s isn't empty but has no PG_VERSION, so it isn't a database this can open; move it aside to start over: %w", data, err)
	}

	if got := strings.TrimSpace(string(version)); got != postgresMajorVersion {
		return fmt.Errorf("%w: %s holds a Postgres %s database, and this build runs Postgres %s", ErrOtherVersion, data, got, postgresMajorVersion)
	}

	return nil
}

func postgresPassword(dir string) (string, error) {
	path := filepath.Join(dir, "postgres-password")

	if data, err := os.ReadFile(filepath.Clean(path)); err == nil {
		return strings.TrimSpace(string(data)), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	b := make([]byte, 24)
	_, _ = rand.Read(b)
	password := hex.EncodeToString(b)

	return password, writeNew(path, password+"\n")
}

func freePort(ctx context.Context) (uint32, error) {
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}

	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		return 0, err
	}

	if port <= 0 || port > 65535 {
		return 0, fmt.Errorf("the system gave port %d", port)
	}

	return uint32(port), nil
}

// writeNew writes a file only its owner can read, failing if it exists.
func writeNew(path, content string) error {
	f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}

	if _, err := io.WriteString(f, content); err != nil {
		_ = f.Close()
		return err
	}

	return f.Close()
}

// logWriter passes Postgres's output to the debug log.
type logWriter struct{ log *slog.Logger }

func (w *logWriter) Write(p []byte) (int, error) {
	for line := range strings.SplitSeq(strings.TrimSpace(string(p)), "\n") {
		if line != "" {
			w.log.Debug("postgres", "line", line)
		}
	}

	return len(p), nil
}

// CheckNotRunning fails if another server answers on the directory's socket,
// since two servers can't share one database.
func CheckNotRunning(dir string) error {
	conn, err := (&net.Dialer{Timeout: time.Second}).Dial("unix", SocketPath(dir))
	if err != nil {
		return nil
	}

	_ = conn.Close()

	return fmt.Errorf("orca-tasks is already running from %s", dir)
}

// stopOrphan stops a Postgres that a killed server left running on the data
// directory, which would otherwise hold its lock. Call it only after
// CheckNotRunning, so it never stops a live server's database.
func stopOrphan(ctx context.Context, dir, data string, log *slog.Logger) {
	if _, err := os.Stat(filepath.Join(data, "postmaster.pid")); err != nil {
		return
	}

	if _, err := os.Stat(filepath.Join(dir, "postgres-bin", "bin", "pg_ctl")); err != nil {
		return
	}

	log.Info("stopping a Postgres left running by an earlier server", "data", data)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Relative to the local directory, which a relative command path is
	// resolved against too.
	cmd := exec.CommandContext(ctx, "postgres-bin/bin/pg_ctl", "stop", "-D", "postgres", "-m", "fast")
	cmd.Dir = dir

	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Debug("pg_ctl stop", "error", err, "output", strings.TrimSpace(string(output)))
	}
}
