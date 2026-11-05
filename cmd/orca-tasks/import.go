package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/leightweight/orca-tasks/internal/store"
)

type importCmd struct {
	DatabaseURL string            `name:"database-url" env:"ORCA_TASKS_DATABASE_URL" required:"" placeholder:"URL" help:"Postgres connection URL."`
	Owner       string            `required:"" help:"The user who administers the imported projects, and owns issues and comments with no reporter or author."`
	User        map[string]string `placeholder:"OLD=NEW" help:"Map a username in the file to a user or agent here. Repeatable; others are used as they are."`
	DryRun      bool              `help:"Check the file and the import, then roll it back."`

	File string `arg:"" help:"The export file, or - for stdin."`
}

func (c *importCmd) Run(ctx context.Context) error {
	var in io.Reader = os.Stdin

	if c.File != "-" {
		f, err := os.Open(filepath.Clean(c.File))
		if err != nil {
			return usagef("%w", err)
		}
		defer func() { _ = f.Close() }()

		in = f
	}

	var file store.ImportFile
	if err := json.NewDecoder(in).Decode(&file); err != nil {
		return usagef("reading the export: %w", err)
	}

	s, err := store.Open(ctx, c.DatabaseURL)
	if err != nil {
		return err
	}
	defer s.Close()

	if err := s.Migrate(ctx); err != nil {
		return err
	}

	catalog, err := s.LoadCatalog(ctx)
	if err != nil {
		return err
	}

	results, err := s.Import(ctx, catalog, file, store.ImportOptions{Owner: c.Owner, Users: c.User, DryRun: c.DryRun})
	if err != nil && !errors.Is(err, store.ErrDryRun) {
		return usagef("nothing was imported:\n%w", err)
	}

	verb := "Imported"
	if c.DryRun {
		verb = "Would import"
	}

	for _, r := range results {
		if _, err := fmt.Printf("%s %s: %d issues, %d comments\n", verb, r.Key, r.Issues, r.Comments); err != nil {
			return err
		}
	}

	return nil
}
