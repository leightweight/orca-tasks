package local

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		want Paths
	}{
		{
			name: "XDG set",
			env:  map[string]string{"XDG_DATA_HOME": "/xdg/data", "XDG_CACHE_HOME": "/xdg/cache", "XDG_RUNTIME_DIR": "/run/user/501"},
			want: Paths{Data: "/xdg/data/orca-tasks", Cache: "/xdg/cache/orca-tasks", Runtime: "/run/user/501/orca-tasks"},
		},
		{
			name: "nothing set",
			env:  map[string]string{},
			want: Paths{Data: "/home/me/.local/share/orca-tasks", Cache: "/home/me/.cache/orca-tasks", Runtime: "/tmp/orca-tasks-501"},
		},
		{
			// The specification says to ignore relative paths.
			name: "relative values",
			env:  map[string]string{"XDG_DATA_HOME": "data", "XDG_RUNTIME_DIR": "run"},
			want: Paths{Data: "/home/me/.local/share/orca-tasks", Cache: "/home/me/.cache/orca-tasks", Runtime: "/tmp/orca-tasks-501"},
		},
		{
			name: "one directory",
			env:  map[string]string{"ORCA_TASKS_LOCAL_DIR": "/srv/tasks", "XDG_DATA_HOME": "/xdg/data"},
			want: Paths{Data: "/srv/tasks", Cache: "/srv/tasks/cache", Runtime: "/srv/tasks/run"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := resolve(func(key string) string { return test.env[key] }, "/home/me", "/tmp", 501)
			if got != test.want {
				t.Errorf("resolve = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestPrepare(t *testing.T) {
	t.Parallel()

	// macOS limits socket paths to 104 bytes, which t.TempDir() can exceed.
	dir, err := os.MkdirTemp("", "ot")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	p := Paths{Data: filepath.Join(dir, "data"), Cache: filepath.Join(dir, "cache"), Runtime: filepath.Join(dir, "run")}

	if err := p.Prepare(); err != nil {
		t.Fatal(err)
	}

	for _, d := range []string{p.Data, p.Cache, p.Runtime} {
		if info, err := os.Stat(d); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s: mode %v, %v; want 0700", d, info.Mode().Perm(), err)
		}
	}

	// Someone else could have made a runtime directory in a shared temporary
	// directory, so an open one is refused.
	if err := os.Chmod(p.Runtime, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := p.Prepare(); !errors.Is(err, errNotPrivate) {
		t.Errorf("an open runtime directory: %v, want errNotPrivate", err)
	}

	long := Paths{Data: p.Data, Cache: p.Cache, Runtime: filepath.Join(dir, strings.Repeat("x", 120))}
	if err := long.Prepare(); err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Errorf("a socket path over the limit: %v", err)
	}
}
