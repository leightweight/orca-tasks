// Command orca-tasks is a task tracker that serves a Jira-compatible API, and
// the CLI that talks to it.
package main

import (
	"fmt"
	"os"

	"github.com/alecthomas/kong"
)

// version is set at build time.
var version = "dev"

type command struct {
	Version kong.VersionFlag `help:"Print the version and exit."`
}

func main() {
	var cmd command

	parser, err := kong.New(&cmd,
		kong.Name("orca-tasks"),
		kong.Description("A task tracker that serves a Jira-compatible API to Orca, and the CLI for it."),
		kong.Vars{"version": version},
		kong.UsageOnError(),
	)
	if err != nil {
		panic(err)
	}

	ctx, err := parser.Parse(os.Args[1:])
	if err != nil {
		parser.FatalIfErrorf(err)
	}

	if err := ctx.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "orca-tasks:", err)
		os.Exit(1)
	}
}
