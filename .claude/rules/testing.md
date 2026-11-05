# Testing

- Tests use Go's `testing` package.
- Store, API, and web tests run against a real Postgres that testcontainers-go starts, so they need Docker.
  Don't mock the database.
- Test the code with logic in it: JQL parsing and evaluation, access rules, API key and session handling, and the Jira response shapes Orca reads.
- Always run tests through mise tasks, never by running `go test` or test binaries directly.
