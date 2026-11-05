# Commands

Run everything through mise tasks.
If a task you need doesn't exist, add it to `.config/mise/config.toml` and list it in this file instead of running the command directly.

## Setup

- `mise bootstrap`: set up a fresh checkout.
  It runs the `bootstrap` task, which installs the git hooks.
- Before `mise run` and `mise exec`, mise downloads Go module dependencies if they changed (`[deps.go]` with `auto = true`).

## Development

- `mise run build`: build the binary into `bin/orca-tasks`.
- `mise run generate`: regenerate the query code in `internal/store/db` from the migrations and queries.
  Run it after changing any `.sql` file.
  `mise run lint` fails when the generated code is out of date.
- `mise run test`: run the tests.
  They start a Postgres container, so Docker must be running.
  The task points testcontainers at the current Docker context, so Colima works without setting `DOCKER_HOST`.
  The whole run has a 2-minute limit, and each test step has its own deadline of a few seconds.
- `mise run lint`: run all linters.
- `mise run lint-fix` (alias `fix`): run all linters and apply fixes.
- `mise tasks ls`: list all available tasks.

## Configuration

`.config/mise/config.toml` pins the tool versions and defines the tasks.
File tasks live in `.config/mise/tasks/`.
Linter configuration lives in `.config/hk.pkl`, `.config/rumdl.toml`, and `.golangci.yml`.
sqlc reads `.config/sqlc.yaml`.
`mise run lint-fix` formats SQL with steveql-fmt.
