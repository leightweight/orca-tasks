package main

import (
	"context"
	"errors"

	"github.com/leightweight/orca-tasks/internal/local"
	"github.com/leightweight/orca-tasks/internal/store"
)

// adminDatabase is the database flags the admin commands share: a URL, or
// --local for the embedded database of `orca-tasks serve --local`.
type adminDatabase struct {
	Local       bool   `help:"Use the database of orca-tasks serve --local on this machine. The server must be stopped."`
	DatabaseURL string `name:"database-url" env:"ORCA_TASKS_DATABASE_URL" placeholder:"URL" help:"Postgres connection URL. Required without --local."`
}

// open connects to the database and migrates it, returning a function that
// disconnects, and with --local, stops the embedded Postgres.
func (a *adminDatabase) open(ctx context.Context) (*store.Store, func(), error) {
	url := a.DatabaseURL
	stop := func() {}

	switch {
	case a.Local && url != "":
		return nil, nil, usagef("--local uses its own database, so leave out --database-url and ORCA_TASKS_DATABASE_URL")
	case a.Local:
		log, err := newLogger("warn", "text")
		if err != nil {
			return nil, nil, err
		}

		db, err := local.OpenDatabase(ctx, log)
		if errors.Is(err, local.ErrInUse) {
			return nil, nil, usagef("stop orca-tasks serve --local first, since its database can only have one user at a time: %w", err)
		}

		if err != nil {
			return nil, nil, err
		}

		url = db.URL
		stop = func() { _ = db.Close() }
	case url == "":
		return nil, nil, usagef("--database-url is required, unless you use --local")
	}

	s, err := store.Open(ctx, url)
	if err != nil {
		stop()
		return nil, nil, err
	}

	if err := s.Migrate(ctx); err != nil {
		s.Close()
		stop()

		return nil, nil, err
	}

	return s, func() { s.Close(); stop() }, nil
}
