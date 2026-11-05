# Commands

Run everything through mise tasks.
If a task you need doesn't exist, add it to `.config/mise/config.toml` and list it in this file instead of running the command directly.

## Setup

- `mise bootstrap`: set up a fresh checkout.
  It runs the `bootstrap` task, which installs the git hooks.
- Before `mise run` and `mise exec`, mise downloads Go module dependencies if they changed (`[deps.go]` with `auto = true`).

## Development

- `mise run build`: build the binary into `bin/orca-tasks`.
- `mise run test`: run the tests.
  The whole run has a 2-minute limit, and each test step has its own deadline of a few seconds.
- `mise run lint`: run all linters.
- `mise run lint-fix` (alias `fix`): run all linters and apply fixes.
- `mise tasks ls`: list all available tasks.

## Configuration

`.config/mise/config.toml` pins the tool versions and defines the tasks.
Linter configuration lives in `.config/hk.pkl`, `.config/rumdl.toml`, and `.golangci.yml`.
