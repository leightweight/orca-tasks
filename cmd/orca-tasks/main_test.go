package main

import (
	"testing"

	"github.com/alecthomas/kong"
)

// TestCommandLine builds the parser, which checks every command's tags, and
// parses a line for each command.
func TestCommandLine(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"serve", "--local"},
		{"serve", "--database-url", "postgres://x", "--base-url", "http://x", "--secret-key", "k", "--log-format", "text"},
		{"admin", "reset-link", "--database-url", "postgres://x", "--base-url", "http://x", "alice"},
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
