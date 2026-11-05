package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/leightweight/orca-tasks/internal/auth"
	"github.com/leightweight/orca-tasks/internal/cli"
	"github.com/leightweight/orca-tasks/internal/server"
	"github.com/leightweight/orca-tasks/internal/store/db"
	"github.com/leightweight/orca-tasks/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithPostgres(m))
}

type commands struct {
	Catalog cli.Catalog `cmd:""`
	Project struct {
		List cli.ProjectList `cmd:"" default:"withargs"`
	} `cmd:""`
	Task struct {
		List    cli.TaskList    `cmd:"" default:"withargs"`
		Show    cli.TaskShow    `cmd:""`
		Create  cli.TaskCreate  `cmd:""`
		Edit    cli.TaskEdit    `cmd:""`
		Move    cli.TaskMove    `cmd:""`
		Comment cli.TaskComment `cmd:""`
		Attach  cli.TaskAttach  `cmd:""`
	} `cmd:""`
}

// newServer runs a server where alice has project OPS and an agent, claude,
// and returns the URL and claude's API key.
func newServer(t *testing.T) (string, string) {
	t.Helper()

	listener := httptest.NewUnstartedServer(nil)
	base := "http://" + listener.Listener.Addr().String()
	s := testutil.Store(t)
	ctx := testutil.Context(t)

	app, err := server.Build(ctx, s, server.Config{
		BaseURL:   base,
		SecretKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		DataDir:   t.TempDir(),
		MaxUpload: 1 << 20,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	listener.Config.Handler = app.Handler
	listener.Start()
	t.Cleanup(listener.Close)

	hash := "unused"

	alice, err := s.CreateUser(ctx, db.CreateUserParams{Username: "alice", DisplayName: "Alice", PasswordHash: &hash, TotpSecret: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}

	claude, err := s.CreateAgent(ctx, alice.ID, "claude", "Claude")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.CreateProject(ctx, alice.ID, "OPS", "Operations"); err != nil {
		t.Fatal(err)
	}

	key, keyHash := auth.NewAPIKey()
	if _, err := s.CreateAPIKey(ctx, db.CreateAPIKeyParams{PrincipalID: claude.ID, Name: "cli", KeyHash: keyHash, AllProjects: true}); err != nil {
		t.Fatal(err)
	}

	return base, key
}

// run runs a command line and returns what it printed.
func run(t *testing.T, base, key, stdin string, args ...string) (string, error) {
	t.Helper()

	var cmd commands

	parser, err := kong.New(&cmd, kong.Exit(func(int) { t.Fatalf("kong exited on %q", args) }))
	if err != nil {
		t.Fatal(err)
	}

	kctx, err := parser.Parse(append(args, "--url", base, "--token", key))
	if err != nil {
		t.Fatalf("parsing %q: %v", args, err)
	}

	var out bytes.Buffer

	kctx.BindTo(testutil.Context(t), (*context.Context)(nil))
	kctx.Bind(&cli.Env{Stdout: &out, Stdin: strings.NewReader(stdin)})

	err = kctx.Run()

	return out.String(), err
}

func mustRun(t *testing.T, base, key, stdin string, args ...string) string {
	t.Helper()

	out, err := run(t, base, key, stdin, args...)
	if err != nil {
		t.Fatalf("%q: %v", args, err)
	}

	return out
}

func TestTaskLifecycle(t *testing.T) {
	t.Parallel()

	base, key := newServer(t)

	out := mustRun(t, base, key, "", "task", "create", "ops", "Rotate the keys", "--type", "bug", "--priority", "High", "--label", "security")
	if !strings.Contains(out, "OPS-1") {
		t.Fatalf("create printed %q", out)
	}

	mustRun(t, base, key, "", "task", "edit", "OPS-1", "--priority", "", "--assign-to-me", "--add-label", "blocked")
	mustRun(t, base, key, "Started on staging.\n", "task", "move", "OPS-1", "in progress", "--comment", "-")
	mustRun(t, base, key, "", "task", "comment", "OPS-1", "Staging is done.")

	file := filepath.Join(t.TempDir(), "diagram.png")
	if err := os.WriteFile(file, []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0o600); err != nil {
		t.Fatal(err)
	}

	if out := mustRun(t, base, key, "", "task", "attach", "OPS-1", file); !strings.HasPrefix(out, "![diagram.png](http") {
		t.Errorf("attach printed %q", out)
	}

	var detail struct {
		Status      string   `json:"status"`
		Priority    string   `json:"priority"`
		Assignee    string   `json:"assignee"`
		Reporter    string   `json:"reporter"`
		Labels      []string `json:"labels"`
		Attachments []any    `json:"attachments"`
		Comments    []struct {
			Author string `json:"author"`
			Body   string `json:"body"`
		} `json:"comments"`
	}

	if err := json.Unmarshal([]byte(mustRun(t, base, key, "", "task", "show", "OPS-1", "-f", "json")), &detail); err != nil {
		t.Fatal(err)
	}

	if detail.Status != "In Progress" || detail.Priority != "" || detail.Assignee != "alice" || detail.Reporter != "alice/claude" ||
		len(detail.Labels) != 2 || len(detail.Comments) != 2 || detail.Comments[0].Author != "alice/claude" || len(detail.Attachments) != 1 {
		t.Errorf("show = %+v", detail)
	}

	if out := mustRun(t, base, key, "", "task", "list", "assignee = currentUser() AND statusCategory != Done"); !strings.Contains(out, "OPS-1") {
		t.Errorf("list printed %q", out)
	}

	if out := mustRun(t, base, key, "", "catalog"); !strings.Contains(out, "alice") || strings.Contains(out, "claude") || !strings.Contains(out, "Operations") {
		t.Errorf("catalog printed %q", out)
	}
}

func TestBadInputExitsWithUsage(t *testing.T) {
	t.Parallel()

	base, key := newServer(t)
	mustRun(t, base, key, "", "task", "create", "OPS", "Rotate the keys")

	for _, args := range [][]string{
		{"task", "move", "OPS-1", "Shipped"},
		{"task", "create", "OPS", "x", "--type", "Saga"},
		{"task", "create", "NOPE", "x"},
		{"task", "list", "project ="},
	} {
		_, err := run(t, base, key, "", args...)
		if !errors.As(err, new(cli.UsageError)) {
			t.Errorf("%q: %v, want a usage error", args, err)
		}
	}

	_, err := run(t, base, key, "", "task", "move", "OPS-1", "Shipped")
	if err == nil || !strings.Contains(err.Error(), `"Done"`) {
		t.Errorf("an unknown status should list the valid ones: %v", err)
	}
}
