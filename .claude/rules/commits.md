# Commits and pull requests

## Conventional Commits (required)

PR titles must follow Conventional Commits.
Intermediate commit subjects should too, even though squash merges replace them with the PR title.

Format: `<type>[optional scope][optional !]: <description>`

Types:

- `feat`: new user-visible behavior.
- `fix`: bug fixes to server or CLI behavior (not CI, docs, or tooling).
- `refactor`: code changes with no behavior change.
- `perf`: performance improvements.
- `test`: test-only changes.
- `docs`: documentation changes.
- `ci`: CI and automation changes.
- `chore`: maintenance, releases, dependency updates, and tooling.
- `security`: security-related changes.
- `revert`: reverting a previous change.

Scopes:

- `jira`: the Jira-compatible REST API that Orca and the CLI call.
- `jql`: the JQL parser and evaluator.
- `store`: the Postgres schema, migrations, and queries.
- `auth`: accounts, sessions, passwords, agents, and API keys.
- `web`: the web interface.
- `cli`: the task commands.
- `chart`: the Helm chart.
- `deps`: dependency updates.

Description style:

- Start with a lowercase word or an acronym such as `JQL` or `API`.
- Use imperative mood ("add", not "added").
- Keep it concise but descriptive.

Examples:

- `feat(jira): support the assignee field in search`
- `fix(auth): expire sessions after a password change`
- `chore(deps): bump jackc/pgx`

## PR titles and descriptions are release notes

Release notes come from PR titles and descriptions, so write them for a user who hasn't read the diff.
Lead with the user-visible problem and outcome.
Show a concrete example, such as a CLI command and its output, when it helps.
Keep the description in sync with the final diff.
Write descriptions in normal paragraphs, with each paragraph on one line.
GitHub shows a single newline in a description as a line break, so the one-sentence-per-line rule for Markdown files doesn't apply.
The same goes for issues and comments.

## Pre-commit

`.config/hk.pkl` defines a git `pre-commit` hook that runs the linters with auto-fix.
Its `stash = "git"` setting stashes unstaged changes while the hook runs, so fixes only touch staged content.
Run `mise run lint-fix` and stage any fixes before committing.
