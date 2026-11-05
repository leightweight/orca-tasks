// Package local runs orca-tasks on one person's machine: an embedded Postgres,
// attachments, and a secret key in the XDG data directory, and a socket that
// lets the person's own agents use the API without an API key.
package local

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres/v2"
	"github.com/gofrs/flock"
	"github.com/google/renameio/v2"
)

// PostgresVersion is the Postgres the local database runs. embedded-postgres
// refuses a data directory made by another major version rather than upgrade
// or erase it, so changing this needs a migration of existing data.
const PostgresVersion = embeddedpostgres.V18

// SecretKey reads the secret key in a data directory, creating one the first
// time.
func SecretKey(dir string) (string, error) {
	return readOrCreate(filepath.Join(dir, "secret-key"), func() string {
		key := make([]byte, 32)
		_, _ = rand.Read(key)

		return base64.StdEncoding.EncodeToString(key)
	})
}

// Postgres is the embedded database.
type Postgres struct {
	URL string
	db  *embeddedpostgres.EmbeddedPostgres
}

// StartPostgres starts Postgres on a free port on 127.0.0.1, with its data in
// the data directory and its binaries in the cache, which it downloads the
// first time. A supervisor process stops Postgres if this one dies, even from
// kill -9, so a killed server never leaves its database running.
func StartPostgres(p Paths, log *slog.Logger) (*Postgres, error) {
	password, err := postgresPassword(p.Data)
	if err != nil {
		return nil, err
	}

	data := filepath.Join(p.Data, "postgres")

	db := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(PostgresVersion).
		Port(0).
		Username("orca").
		Password(password).
		Database("orca").
		DataPath(data).
		// Each start works in a new directory under this one.
		RuntimePath(filepath.Join(p.Runtime, "postgres")).
		CachePath(filepath.Join(p.Cache, "postgres")).
		StartTimeout(time.Minute).
		Logger(&logWriter{log: log}))

	// Start rather than StartContext, which would stop Postgres as soon as the
	// server's context ends, before the server has finished with it.
	if err := db.Start(); err != nil {
		if errors.Is(err, embeddedpostgres.ErrDataVersion) {
			return nil, fmt.Errorf("%s holds a database from another Postgres version, which this build won't open: %w", data, err)
		}

		return nil, fmt.Errorf("starting Postgres: %w", err)
	}

	return &Postgres{URL: db.ConnectionURL(), db: db}, nil
}

// Stop stops Postgres.
func (p *Postgres) Stop() error { return p.db.Close() }

func postgresPassword(dir string) (string, error) {
	return readOrCreate(filepath.Join(dir, "postgres-password"), func() string {
		b := make([]byte, 24)
		_, _ = rand.Read(b)

		return hex.EncodeToString(b)
	})
}

// readOrCreate reads a generated secret, or generates and saves one the first
// time. The file is written to a temporary file and renamed into place, so a
// crash can't leave it half written, and only its owner can read it. An empty
// file is refused rather than trusted, since a server needs the value it held.
func readOrCreate(path string, generate func() string) (string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err == nil {
		value := strings.TrimSpace(string(data))
		if value == "" {
			return "", fmt.Errorf("%s is empty, so it doesn't hold the value this server was set up with", path)
		}

		return value, nil
	}

	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	value := generate()

	return value, renameio.WriteFile(path, []byte(value+"\n"), 0o600)
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

// ErrInUse means another server or admin command holds the data directory.
var ErrInUse = errors.New("another orca-tasks server or admin command is using the data directory")

// Lock is an exclusive lock on a data directory, held for as long as a process
// uses its database.
type Lock struct{ file *flock.Flock }

// LockData takes the data directory's lock, or fails with ErrInUse if another
// process holds it. Two processes can't share one Postgres: a second would
// stop the first's database as an orphan, or on a first run, embedded-postgres
// could remove the directory the first is creating. The lock is the data
// directory's, not the socket's, so processes started with different
// runtime directories still see each other.
func LockData(p Paths) (*Lock, error) {
	file := flock.New(filepath.Join(p.Data, "lock"), flock.SetPermissions(0o600))

	locked, err := file.TryLock()
	if err != nil {
		return nil, err
	}

	if !locked {
		return nil, fmt.Errorf("%w: %s", ErrInUse, p.Data)
	}

	return &Lock{file: file}, nil
}

// Release gives the lock up.
func (l *Lock) Release() error { return l.file.Unlock() }

// Database is the local mode database, running and locked until Close.
type Database struct {
	Paths Paths
	URL   string

	postgres *Postgres
	lock     *Lock
}

// OpenDatabase resolves the local directories, takes the data directory's
// lock, and starts Postgres. The server and the admin commands both use it.
func OpenDatabase(log *slog.Logger) (*Database, error) {
	paths, err := Resolve()
	if err != nil {
		return nil, err
	}

	if err := paths.Prepare(); err != nil {
		return nil, err
	}

	lock, err := LockData(paths)
	if err != nil {
		return nil, err
	}

	log.Info("starting Postgres; the first run downloads it", "data", paths.Data, "cache", paths.Cache, "runtime", paths.Runtime)

	pg, err := StartPostgres(paths, log)
	if err != nil {
		_ = lock.Release()
		return nil, err
	}

	return &Database{Paths: paths, URL: pg.URL, postgres: pg, lock: lock}, nil
}

// Close stops Postgres and releases the lock.
func (d *Database) Close() error {
	err := d.postgres.Stop()

	return errors.Join(err, d.lock.Release())
}
