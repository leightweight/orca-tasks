package main

import (
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

// TestCommandLine builds the parser, which checks every command's tags, and
// parses a line for each command.
func TestCommandLine(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"serve", "--local"},
		{"serve", "--local", "--addr", "127.0.0.1:9000"},
		{"serve", "--database-url", "postgres://x", "--base-url", "http://x", "--secret-key", "k", "--log-format", "text"},
		{"admin", "reset-link", "--database-url", "postgres://x", "--base-url", "http://x", "alice"},
		{"admin", "reset-link", "--local", "alice"},
		{"admin", "import", "--local", "--owner", "alice", "-"},
		{"admin", "import", "--database-url", "postgres://x", "--owner", "alice", "--user", "claude=alice/claude", "-"},
		{"task", "list", "--url", "http://x", "--token", "otk_x"},
		{"task", "move", "OPS-1", "Done", "--assign-to-me"},
		{"catalog"},
	} {
		var cmd command

		parser, err := newParser(&cmd, kong.Exit(func(int) { t.Fatalf("kong exited on %q", args) }))
		if err != nil {
			t.Fatal(err)
		}

		if _, err := parser.Parse(args); err != nil {
			t.Errorf("parsing %q: %v", args, err)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	t.Parallel()

	for addr, want := range map[string]bool{
		"127.0.0.1:8080": true,
		"localhost:8080": true,
		"[::1]:8080":     true,
		":8080":          false,
		"0.0.0.0:8080":   false,
		"192.168.1.5:80": false,
		"127.0.0.1":      false,
	} {
		if got := isLoopback(addr); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestParseFailurePointsToTheCommandsHelp(t *testing.T) {
	t.Parallel()

	var cmd command

	parser, err := newParser(&cmd, kong.Exit(func(int) { t.Fatal("kong exited") }))
	if err != nil {
		t.Fatal(err)
	}

	_, err = parser.Parse([]string{"task", "list", "--format", "yaml"})
	if err == nil {
		t.Fatal("an unknown format parsed")
	}

	if got := parseFailure(err); !strings.Contains(got, "Run orca-tasks task list --help") {
		t.Errorf("parseFailure = %q", got)
	}
}
