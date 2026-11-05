package local

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// Paths are the directories local mode uses, following the XDG Base Directory
// specification.
type Paths struct {
	// Data holds what's worth keeping: the database, its password,
	// attachments, and the secret key. Backing up local mode is copying it.
	Data string

	// Cache holds the downloaded Postgres, which another download replaces.
	Cache string

	// Runtime holds the agent socket and Postgres's runtime files, which
	// matter only while the server runs.
	Runtime string
}

// maxSocketPath is the longest Unix socket path macOS accepts; Linux accepts
// one byte more.
const maxSocketPath = 103

// Resolve finds the local mode directories from the environment:
//
//   - $ORCA_TASKS_LOCAL_DIR puts everything under one directory.
//   - Otherwise data goes in $XDG_DATA_HOME, which defaults to ~/.local/share,
//     the cache in $XDG_CACHE_HOME, which defaults to ~/.cache, and runtime
//     files in $XDG_RUNTIME_DIR, each in an orca-tasks directory.
//   - Without $XDG_RUNTIME_DIR, which macOS never sets, runtime files go in
//     the temporary directory, in a directory named for the user, as the
//     specification suggests.
func Resolve() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}

	return resolve(os.Getenv, home, os.TempDir(), os.Getuid()), nil
}

func resolve(getenv func(string) string, home, tmp string, uid int) Paths {
	if dir := getenv("ORCA_TASKS_LOCAL_DIR"); dir != "" {
		return Paths{Data: dir, Cache: filepath.Join(dir, "cache"), Runtime: filepath.Join(dir, "run")}
	}

	base := func(variable string, fallback ...string) string {
		if dir := getenv(variable); filepath.IsAbs(dir) {
			return dir
		}

		return filepath.Join(fallback...)
	}

	runtime := filepath.Join(tmp, "orca-tasks-"+strconv.Itoa(uid))
	if dir := getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(dir) {
		runtime = filepath.Join(dir, "orca-tasks")
	}

	return Paths{
		Data:    filepath.Join(base("XDG_DATA_HOME", home, ".local", "share"), "orca-tasks"),
		Cache:   filepath.Join(base("XDG_CACHE_HOME", home, ".cache"), "orca-tasks"),
		Runtime: runtime,
	}
}

// Socket is the agent socket.
func (p Paths) Socket() string { return filepath.Join(p.Runtime, "agent.sock") }

// Attachments is where attachment contents go.
func (p Paths) Attachments() string { return filepath.Join(p.Data, "attachments") }

// Prepare creates the directories, readable only by their owner. The runtime
// directory may be in a shared temporary directory, so one that already
// exists must belong to this user and be private, or someone else could have
// made it to read the socket.
func (p Paths) Prepare() error {
	if len(p.Socket()) > maxSocketPath {
		return fmt.Errorf("the socket path %s is longer than the %d bytes a Unix socket allows; "+
			"set XDG_RUNTIME_DIR or ORCA_TASKS_LOCAL_DIR to a shorter directory", p.Socket(), maxSocketPath)
	}

	for _, dir := range []string{p.Data, p.Cache, p.Runtime} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}

	return checkPrivate(p.Runtime)
}

var errNotPrivate = errors.New("the runtime directory must belong to you and be readable only by you")

func checkPrivate(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("%w: %s is %v", errNotPrivate, dir, info.Mode())
	}

	return nil
}
