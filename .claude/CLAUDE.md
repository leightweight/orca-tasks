# orca-tasks

A self-hosted task tracker that serves a Jira-compatible API, so Orca's Tasks page and Claude Code agents can share one task list.

[`.claude/docs/design.md`](docs/design.md) is the source of truth for the data model, access rules, Jira API, web interface, and CLI.

Project instructions live in [`.claude/rules/`](rules/), one topic per file.
Rules with `paths` frontmatter load only when working on matching files.

Task-specific procedures will live as skills in `.claude/skills/`.
When a rule points to a skill, load it before doing that task.
