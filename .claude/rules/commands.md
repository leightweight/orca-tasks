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
- `mise run docker-build [image]`: build the image from source.
- `mise run up`: build the image and run it with Postgres through `compose.yml`, on <http://localhost:8090>.
  `mise run down` stops them, and `mise run down --wipe` also deletes their data.
  mise installs `docker-compose`, so the tasks don't depend on a Compose plugin.
- `mise run release:snapshot`: build the release binaries and images locally without publishing them.
  The output goes in `.cache/dist/`.
- Pushing a `v*` tag publishes a release through `.github/workflows/release.yml`.
  Only push release tags when the user asks.
- `mise tasks ls`: list all available tasks.

## Configuration

`.config/mise/config.toml` pins the tool versions and defines the tasks.
File tasks live in `.config/mise/tasks/`.
Linter configuration lives in `.config/hk.pkl`, `.config/rumdl.toml`, and `.golangci.yml`.
The `helm` step lints the chart with `charts/orca-tasks/ci/lint-values.yaml`, and the `goreleaser` step checks `.goreleaser.yaml`.
sqlc reads `.config/sqlc.yaml`.
`mise run lint-fix` formats SQL with steveql-fmt.
