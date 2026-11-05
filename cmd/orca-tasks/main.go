// Command orca-tasks is a task tracker that serves a Jira-compatible API, and
// the CLI that talks to it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"

	"github.com/leightweight/orca-tasks/internal/cli"
)

// version is set at build time.
var version = "dev"

type command struct {
	Version kong.VersionFlag `help:"Print the version and exit."`

	Catalog cli.Catalog `cmd:"" help:"List the projects, statuses, types, priorities, and users the other commands accept."`
	Project projectCmd  `cmd:"" help:"List projects."`
	Task    taskCmd     `cmd:"" help:"List, show, create, and change tasks."`

	Serve serveCmd `cmd:"" help:"Run the server."`
	Admin adminCmd `cmd:"" help:"Administer the server from inside its container."`
}

type projectCmd struct {
	List cli.ProjectList `cmd:"" default:"withargs" help:"List the projects your key can reach. The default."`
}

// taskCmd groups the commands that do what Orca's Tasks page does. Orca calls
// them tasks; the API calls them issues.
type taskCmd struct {
	List    cli.TaskList    `cmd:"" default:"withargs" help:"List tasks, optionally filtered with JQL. The default."`
	Show    cli.TaskShow    `cmd:"" help:"Show a task with its description, comments, and attachments."`
	Create  cli.TaskCreate  `cmd:"" help:"Create a task and print it."`
	Edit    cli.TaskEdit    `cmd:"" help:"Change a task's fields or labels."`
	Move    cli.TaskMove    `cmd:"" help:"Move a task to another status."`
	Comment cli.TaskComment `cmd:"" help:"Comment on a task."`
	Attach  cli.TaskAttach  `cmd:"" help:"Attach files to a task and print the Markdown that shows them."`
}

// usageError is a mistake on the command line or in its input, which exits 2.
type usageError struct{ error }

func usagef(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
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

	kctx, err := parser.Parse(os.Args[1:])
	if err != nil {
		parser.FatalIfErrorf(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	kctx.BindTo(ctx, (*context.Context)(nil))
	kctx.Bind(cli.DefaultEnv())

	err = kctx.Run()

	switch {
	case err == nil:
	case errors.Is(err, context.Canceled):
		os.Exit(130)
	case errors.As(err, new(usageError)), errors.As(err, new(cli.UsageError)):
		fmt.Fprintln(os.Stderr, "orca-tasks:", err)
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, "orca-tasks:", err)
		os.Exit(1)
	}
}

func newLogger(level, format string) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, usagef("unknown log level %q", level)
	}

	options := &slog.HandlerOptions{Level: l}

	switch format {
	case "json":
		return slog.New(slog.NewJSONHandler(os.Stderr, options)), nil
	case "text":
		return slog.New(slog.NewTextHandler(os.Stderr, options)), nil
	default:
		return nil, usagef("unknown log format %q", format)
	}
}
