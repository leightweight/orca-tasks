package cli_test

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leightweight/orca-tasks/internal/cli"
	"github.com/leightweight/orca-tasks/internal/server"
	"github.com/leightweight/orca-tasks/internal/store/db"
	"github.com/leightweight/orca-tasks/internal/testutil"
)

// newLocalServer runs a server in local mode, serving only its socket, where
// alice has project OPS. withAgent gives her the "agent" agent, as setup does.
func newLocalServer(t *testing.T, withAgent bool) string {
	t.Helper()

	// macOS limits socket paths to 104 bytes, which t.TempDir() can exceed.
	dir, err := os.MkdirTemp("", "ot")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	socket := filepath.Join(dir, "agent.sock")
	s := testutil.Store(t)
	ctx := testutil.Context(t)

	app, err := server.Build(ctx, s, server.Config{
		BaseURL:   "http://127.0.0.1:8080",
		SecretKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		DataDir:   t.TempDir(),
		MaxUpload: 1 << 20,
		Socket:    socket,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	listener, httpServer, err := app.ListenSocket(testutil.Context(t), socket)
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = httpServer.Serve(listener) }()

	t.Cleanup(func() { _ = httpServer.Close() })

	hash := "unused"

	alice, err := s.CreateUser(ctx, db.CreateUserParams{Username: "alice", DisplayName: "Alice", PasswordHash: &hash, TotpSecret: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}

	if withAgent {
		if _, err := s.CreateAgent(ctx, alice.ID, "agent", "Agent"); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.CreateProject(ctx, alice.ID, "OPS", "Operations"); err != nil {
		t.Fatal(err)
	}

	if info, err := os.Stat(socket); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}

	if _, _, err := app.ListenSocket(testutil.Context(t), socket); err == nil {
		t.Error("a second server took over a live socket")
	}

	return socket
}

func TestTheLocalSocketNeedsNoKey(t *testing.T) {
	t.Parallel()

	socket := newLocalServer(t, true)

	mustRun(t, "", "", "", "task", "create", "OPS", "Rotate the keys", "--socket", socket)
	mustRun(t, "", "", "", "task", "move", "OPS-1", "In Progress", "--assign-to-me", "--socket", socket)

	var detail struct {
		URL      string `json:"url"`
		Reporter string `json:"reporter"`
		Assignee string `json:"assignee"`
	}

	if err := json.Unmarshal([]byte(mustRun(t, "", "", "", "task", "show", "OPS-1", "-f", "json", "--socket", socket)), &detail); err != nil {
		t.Fatal(err)
	}

	if detail.Reporter != "alice/agent" || detail.Assignee != "alice" {
		t.Errorf("OPS-1 = reporter %s, assignee %s; want alice/agent and alice", detail.Reporter, detail.Assignee)
	}

	// The link comes from the server's base URL, not the socket.
	if detail.URL != "http://127.0.0.1:8080/browse/OPS-1" {
		t.Errorf("OPS-1's url is %q", detail.URL)
	}
}

func TestTheLocalSocketWithoutTheAgent(t *testing.T) {
	t.Parallel()

	socket := newLocalServer(t, false)

	_, err := run(t, "", "", "", "project", "list", "--socket", socket)
	if err == nil || !strings.Contains(err.Error(), `no agent named "agent"`) {
		t.Errorf("without the agent: %v", err)
	}

	if _, err := run(t, "", "", "", "project", "list", "--socket", filepath.Join(t.TempDir(), "missing.sock")); !errors.As(err, new(cli.UsageError)) {
		t.Errorf("a missing socket: %v, want a usage error", err)
	}
}
