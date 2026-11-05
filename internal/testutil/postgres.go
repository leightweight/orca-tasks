package testutil

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tclog "github.com/testcontainers/testcontainers-go/log"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/leightweight/orca-tasks/internal/store"
)

// PostgresImage is the Postgres the tests run against.
const PostgresImage = "postgres:18-alpine"

const templateName = "orca_template"

var (
	adminURL  string
	databases atomic.Int64
)

// RunWithPostgres starts a Postgres container, migrates a template database
// in it, runs the package's tests, and removes the container. Call it from
// TestMain in packages whose tests use Store.
func RunWithPostgres(m *testing.M) int {
	tclog.SetDefault(log.New(io.Discard, "", 0))

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	container, err := postgres.Run(ctx, PostgresImage,
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		postgres.BasicWaitStrategies(),
		// Durability only slows tests down; the data is thrown away.
		testcontainers.WithCmdArgs("-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off"),
		testcontainers.WithTmpfs(map[string]string{"/var/lib/postgresql": "rw"}),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "starting Postgres:", err)
		return 1
	}

	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_ = testcontainers.TerminateContainer(container, testcontainers.StopContext(stop))
	}()

	adminURL, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintln(os.Stderr, "reading the Postgres address:", err)
		return 1
	}

	if err := createTemplate(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "creating the template database:", err)
		return 1
	}

	return m.Run()
}

func createTemplate(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()

	if _, err := conn.Exec(ctx, "CREATE DATABASE "+templateName); err != nil {
		return err
	}

	s, err := store.Open(ctx, databaseURL(templateName))
	if err != nil {
		return err
	}

	err = s.Migrate(ctx)
	s.Close()

	return err
}

// Store returns a store on a new, migrated database that only this test uses.
func Store(t testing.TB) *store.Store {
	t.Helper()

	if adminURL == "" {
		t.Fatal("call testutil.RunWithPostgres from TestMain to use testutil.Store")
	}

	ctx := Context(t)
	name := fmt.Sprintf("test_%d", databases.Add(1))

	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	if _, err := conn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateName)); err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(ctx, databaseURL(name))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(s.Close)

	return s
}

// databaseURL swaps the database name in the admin URL.
func databaseURL(name string) string {
	config, err := pgx.ParseConfig(adminURL)
	if err != nil {
		panic(err)
	}

	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", config.User, config.Password, config.Host, config.Port, name)
}
