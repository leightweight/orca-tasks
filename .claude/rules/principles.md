# Principles

- [`.claude/docs/design.md`](../docs/design.md) is the source of truth for the data model, access rules, Jira API, web interface, and CLI.
  Read it before changing behavior, and update it in the same change when the design moves.
- Use maintained open source dependencies instead of writing our own, so the project has less code to maintain.
  Before implementing anything generic, such as sessions, password hashing, migrations, CSRF protection, or Markdown rendering, look for a maintained library that does it.
- Every read and write goes through the caller's project memberships.
  A query that returns issues, comments, projects, or users must be scoped to the projects the caller can see.
  Only the site administration pages may see across projects, and they show counts, not task contents.
- Never store a password, an API key, a recovery code, or a TOTP secret in plain text, and never log one.
- Implement the Jira API from Atlassian's public documentation and Orca's behavior, not from Atlassian code or copied documentation text.
  Don't use "Jira" in the product's name or branding.
  Describing the API as Jira-compatible is fine.
