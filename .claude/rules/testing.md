# Testing

- Tests use Go's `testing` package.
- Store, API, and web tests run against a real Postgres that testcontainers-go starts, so they need Docker.
  Don't mock the database.
  Each test package starts one container from `TestMain` with `testutil.RunWithPostgres`, and `testutil.Store(t)` gives each test its own database cloned from a migrated template, so tests run in parallel without seeing each other's rows.
- Test the code with logic in it: JQL parsing and evaluation, access rules, API key and session handling, and the Jira response shapes Orca reads.
- Always run tests through mise tasks, never by running `go test` or test binaries directly.

## Fast, and fails fast

A hung test should fail in seconds, not after the package timeout.

- Give every step its own short deadline.
  Use `testutil.Context(t)` for each database call, request, or command, rather than one context for the whole test.
  Its deadline is a few seconds; a step that needs longer is a bug to fix, not a deadline to raise.
- Test HTTP clients use `testutil.Client`, which has a short timeout.
- Call `t.Parallel()` in every test that doesn't change process-wide state.
- Don't sleep in tests.
  Inject a clock where code depends on time.

The same goes for the code under test.
Load what a request needs in a fixed number of queries, never one query per row, and index every column a query filters or joins on.
