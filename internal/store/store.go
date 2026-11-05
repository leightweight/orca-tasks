// Package store is the Postgres database: its migrations, the queries sqlc
// generates into the db package, and the transactions built from them.
package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/leightweight/orca-tasks/internal/store/db"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is the database.
type Store struct {
	Pool *pgxpool.Pool
	*db.Queries
}

// Open connects to a database. It doesn't migrate it.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connecting to the database: %w", err)
	}

	return &Store{Pool: pool, Queries: db.New(pool)}, nil
}

// Close closes the connections.
func (s *Store) Close() { s.Pool.Close() }

// Migrate applies any migrations the database doesn't have yet. It holds a
// Postgres advisory lock while it works, so replicas starting together apply
// each migration once.
func (s *Store) Migrate(ctx context.Context) error {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}

	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}

	conn := stdlib.OpenDBFromPool(s.Pool)
	defer func() { _ = conn.Close() }()

	provider, err := goose.NewProvider(goose.DialectPostgres, conn, dir,
		goose.WithSessionLocker(locker),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return err
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrating the database: %w", err)
	}

	return nil
}

// InTx runs fn in a transaction, committing if it returns nil.
func (s *Store) InTx(ctx context.Context, fn func(q *db.Queries, tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return fn(s.WithTx(tx), tx)
	})
}
