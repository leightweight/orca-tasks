# orca-tasks design

## Summary

orca-tasks is a self-hosted task tracker.
It stores tasks in Postgres and serves them through a subset of the Jira Server REST API, so Orca's Tasks page can use it as a Jira site.
A CLI calls the same API, so Claude Code agents can read and change the same tasks through a skill.

Several people share one server.
Each has their own login, agents, and API keys, and sees only the projects they belong to.

The web interface manages accounts, projects, and access.
It is not a task board.
People work with tasks in Orca, and agents work with them through the CLI.

## Project

- Repository: `github.com/leightweight/orca-tasks`.
- Go module: `github.com/leightweight/orca-tasks`.
- One binary, `orca-tasks`, holds the server and the CLI.
- License: MIT.
- Container image: `ghcr.io/leightweight/orca-tasks`.
- Helm chart: `oci://ghcr.io/leightweight/charts/orca-tasks`.

## Principles

- Use maintained open source dependencies instead of writing our own, so the project has less code to maintain.
- Every read and write is scoped to the caller's project memberships.
- Never store a password, an API key, or a recovery code in plain text, and never log one.
- Implement only the parts of the Jira API that Orca and the CLI call.
- Keep all state in Postgres and the attachment directory, so a restart loses nothing.

## Concepts

### Principals

A principal is anything that can authenticate and appear on a task as a reporter, assignee, comment author, or attachment uploader.
There are two kinds.

- A user is a person.
  Users sign in to the web interface with a username, a password, and a TOTP code.
- An agent belongs to one user, such as `claude` acting for that user.
  Agents can't sign in to the web interface.
  They authenticate only with API keys.

Usernames are unique across users and agents, because Jira user fields identify a principal by name alone.
A username starts with a lowercase letter and holds only lowercase letters, digits, `.`, `_`, and `-`.
Each principal also has a display name, and users have an optional email address.

A user can be a site administrator.
The first user is one, and site administrators can make others one.

### Projects

A project has a numeric ID, a name, and a key, such as `OPS`.
The ID identifies the project everywhere in the database and the API.
A key starts with an uppercase letter and holds only uppercase letters, digits, and underscores.

Keys aren't unique across the server, so two teams can each have an `OPS` project.
They are unique for each user, through the key each member sees.
See [Project keys](#project-keys).

Issue numbers count up per project and are never reused.

Users are members of projects, with one of two roles.

- A member can read and change every issue in the project.
- A project administrator can also rename the project, add or remove members, and delete the project.

Any user can create a project and becomes its first administrator.

### Memberships

A membership is a row joining a principal to a project, and it is how tasks refer to people.
An issue's reporter and assignee, a comment's author, an attachment's uploader, and a history entry's actor all reference a membership, not a principal.
A composite foreign key on the project ID makes it impossible for an issue to reference a membership in another project.

Removing a member soft-deletes the membership.
The principal loses access to the project, and everything that references the membership still shows who it was.
Adding the principal back restores the same membership, so their earlier work is theirs again.
An issue keeps its assignee when the assignee is removed, and the API reports that user as inactive until someone reassigns it.

Agents get memberships too, so their work is attributed to them.
An agent has a membership in every project its owner is a member of.
The server creates and removes them with the owner's memberships and when the agent is created, so an agent can reach exactly the projects its owner can, under the owner's keys.

### Project keys

Each user's membership has the key that user sees the project under.
It starts as the project's key, and it is unique among the user's active memberships.
An agent's membership uses its owner's key.

When a user is added to a project whose key they already use, the membership gets the key with the lowest free number appended, such as `OPS2`.
The user can change the key for any of their memberships on the projects page, to replace that suffix with something clearer.

An issue's key is the viewer's key for its project followed by the issue number, such as `OPS-12` for one member and `OPS2-12` for another.
The API, JQL, the CLI, and `/browse` links all use the caller's keys.
The site administration pages show the project's own key and its ID.

Two members with different keys for a project see different keys for the same issue, so a key written in a comment means what it meant to its author.
Changing a membership key also changes that user's issue keys, so Orca worktrees linked to the old keys lose their link.

### Issues and comments

An issue has a summary, a Markdown description, a status, a type, an optional priority, an optional assignee, a reporter, labels, and created and updated times.
A comment has a Markdown body, an author, and created and updated times.

Statuses, issue types, and priorities are shared by every project and seeded on first start.

| Statuses | Category |
| --- | --- |
| To Do | new |
| In Progress, In Review, Blocked | indeterminate |
| Done, Cancelled | done |

Issue types are Task, Bug, Story, Epic, and Chore.
Priorities are Highest, High, Medium, Low, and Lowest.

Issues can't be deleted.
Moving one to Cancelled is the way to drop it.

### History

The server records every change to an issue in the same transaction as the change.
An entry has the issue, the actor's membership, the time, the field, and the old and new values.
Creating an issue, adding a comment, and adding or deleting an attachment also get entries.

Nothing reads the history yet.
It is there for a task view in the web interface or the API to show later.

### Deleting a project

A project administrator or a site administrator can delete a project from its page in the web interface, after typing its key to confirm.
Deletion removes the project's memberships, issues, comments, history, and attachment rows in one transaction.
The attachment files go in the next cleanup pass.
There is no undo.

### Attachments

An issue can have attachments.
Each has a numeric ID, a filename, a MIME type, a size, a SHA-256 digest, an uploader, and a created time.
The ID is numeric because Orca finds attachment IDs in descriptions with a pattern that only matches digits.

The server stores metadata in Postgres and contents as files under the data directory, named by ID.
An upload goes to a temporary file first and is renamed into place after the row commits.
At startup, and then daily, the server deletes files with no row and temporary files older than a day.

The uploader and the project's administrators can delete an attachment.
The maximum upload size is configurable and defaults to 25 MiB.

Orca shows images from attachments inline in an issue's description.
It reads the issue's `attachment` field and the rendered description, and downloads each image the description links to.
A description embeds an attachment with Markdown such as `![diagram](https://tasks.example.com/secure/attachment/42/diagram.png)`.

## Access

- A principal sees a project, its issues, comments, and attachments only through an active membership.
- A principal sees another principal, in user searches and on issues, only when they share a project.
  An issue can still show a reporter or comment author whose membership has since been removed.
- A new assignee must have an active membership in the issue's project.
- An API key can be limited further, as described in [API keys](#api-keys).
- Site administrators manage users and see per-user counts.
  They read task contents only through their own memberships, like anyone else.
  A site administrator can add members to any project, so a project whose administrators have all left can be recovered.
- A disabled user can't sign in, and neither the user's API keys nor their agents' keys work.
  Their tasks, comments, and attachments stay, attributed to them.
  Principals are disabled, never deleted, so attribution survives.

## Accounts

### First run

While the database has no users, the server logs a one-time setup code at startup and every page redirects to `/setup`.
The setup form asks for the code, a username, a display name, and a password.
It then enrolls TOTP, as below, and creates a site administrator once the user confirms a code.
The setup code stops someone who reaches a fresh install first from claiming it.

The server creates the first user in a transaction under a Postgres advisory lock that checks again for existing users, so two setup requests at once produce one administrator.

### Sessions and passwords

The web interface uses cookie sessions stored in Postgres through `alexedwards/scs` and its `pgxstore`.
A session lasts 30 days, or 7 without use.
The cookie is `HttpOnly` and `SameSite=Lax`, and `Secure` when the base URL is HTTPS.
`net/http.CrossOriginProtection` rejects cross-origin form posts.

Passwords are hashed with Argon2id through `alexedwards/argon2id`.
A user changes their own password by entering the current one and a TOTP code.
Changing a password ends the user's other sessions.

There is no email.
A site administrator resets an account by generating a one-time link, valid for 24 hours, and handing it to the user.
The link sets a new password and enrolls a new TOTP secret, so it also recovers a lost authenticator.
Creating a user works the same way.
The administrator enters the username and display name and gets a link where the new user sets a password and enrolls TOTP.

If the only administrator is locked out, `orca-tasks admin reset-link <username>` run inside the server's container prints a link.
It connects to the database directly, so it needs no API key.

### Sign-in limits

The server limits failed sign-ins with `sethvargo/go-limiter`, keyed by client IP and separately by username.
It allows 10 failures in 15 minutes for each, counting wrong passwords, TOTP codes, and recovery codes.
Further attempts get HTTP 429 until the window passes.
The limits apply to the setup code and API key authentication too.
Reset links are 256-bit random tokens, so guessing one isn't worth limiting.
The counters are in memory, which is enough for the chart's one replica.

The client IP comes from the [trusted proxies](#client-addresses), so the limit applies per client and not to the ingress as a whole.

### Two-factor authentication

Every user must use TOTP, including the first administrator.
A user has no way to turn it off.

- Enrollment shows a QR code and the secret, and finishes when the user enters a valid code.
  An account that hasn't finished enrollment can't sign in or use API keys.
- Enrollment also shows 10 single-use recovery codes, stored as hashes.
  A recovery code replaces a TOTP code at sign-in.
  A user can generate a new set, which replaces the old one.
- Sign-in asks for the password, then the TOTP code.
- The server accepts a code from one 30-second step either side of now, and records the last step used so a code can't be used twice.
- Creating an API key, changing a password, and generating recovery codes ask for a current TOTP code.

`pquerna/otp` generates secrets, QR codes, and validates codes.
TOTP secrets have to be readable to check codes, so they can't be hashed.
The server encrypts them with `golang.org/x/crypto/nacl/secretbox`, using a key from its configuration.

API keys don't take a TOTP code, because they're for programs.
Only a user who signed in with TOTP can create one.

### Agents

A user creates agents from their account page.
An agent has a username, such as `jleight-claude`, and a display name, such as `Claude`.
The owner can rename, disable, and enable their agents.

### API keys

A user creates API keys for themselves and for each of their agents.
A key has a name, such as `laptop` or `orca`, and an optional expiry.
The server shows the key once, when it is created.

A key can be narrower than its principal, so a key that leaks from a laptop does less damage.

- Its access is read and write, or read-only.
  A read-only key gets HTTP 403 on any route that changes something.
- It reaches all of the principal's projects, including ones joined later, or only the projects chosen when it is created.
  A key limited to projects sees nothing else, not even the other projects' names.

Both are fixed when the key is created.
To change them, create a new key and revoke the old one.
The key list shows each key's access and projects, its last use, and its expiry.

Keys are `otk_` followed by 32 random bytes in base32.
The database stores the SHA-256 hash of each key, which is enough for random keys of this length.
The server records when each key was last used, at most once a minute, to keep writes down.
A user can revoke any of their keys or their agents' keys.

## Web interface

The web interface is server-rendered `html/template` pages embedded in the binary, with one stylesheet and no JavaScript build.
Forms post and redirect.

| Page | Who | Purpose |
| --- | --- | --- |
| `/setup` | Anyone, before the first user exists | Create the first administrator. |
| `/login`, `/logout` | Users | Sign in and out. |
| `/account` | Users | Change display name, email, and password, and generate recovery codes. |
| `/account/keys` | Users | Create and revoke the user's API keys. |
| `/account/agents` | Users | Create and manage agents and their API keys. |
| `/projects` | Users | List the user's projects, change the key the user sees each under, and create a project. |
| `/projects/{key}` | Project members | Show counts, and let administrators rename the project, manage members, and delete it. |
| `/admin/users` | Site administrators | List users with their counts, create users, disable or enable them, grant administration, and generate reset links. |
| `/admin/projects` | Site administrators | List every project with counts and members, add members, and delete projects. |
| `/admin/proxy` | Site administrators | Show the connecting address, the forwarding headers, which hops are trusted, and the client address the server resolved. |
| `/browse/{key}` | Project members | Show one issue with its description, comments, and attachments, read-only. |

Orca links each issue to `{site}/browse/{KEY}`, which is why the read-only issue page exists.

For each user, the user list shows their projects, agents, active API keys, issues reported, issues assigned, comments, and attachments.
It also shows the bytes of description, comment, and attachment data from the user and their agents, the last sign-in, and the last API key use.

## Jira-compatible API

The server implements the routes Orca calls in the Jira Server and Data Center REST API v2, plus the agile board routes Orca uses to order its columns.
Version 2 sends descriptions and comments as plain text, so Markdown reaches Orca unchanged.

| Route | Purpose |
| --- | --- |
| `GET /rest/api/2/myself` | The calling principal. |
| `POST /rest/api/2/search`, `GET /rest/api/2/search` | JQL search. |
| `GET /rest/api/2/project` | Projects the caller can see, under the caller's keys. |
| `GET /rest/api/2/status`, `GET /rest/api/2/issuetype`, `GET /rest/api/2/priority` | The catalog, for the CLI. |
| `GET /rest/api/2/user/search`, `GET /rest/api/2/user/assignable/search` | Principals the caller shares a project with. |
| `GET /rest/api/2/filter/favourite` | Saved filters, empty for now. |
| `GET /rest/api/2/field` | Field metadata. |
| `POST /rest/api/2/issue` | Create an issue. |
| `GET`, `PUT /rest/api/2/issue/{key}` | Read or edit an issue. |
| `PUT /rest/api/2/issue/{key}/assignee` | Assign an issue. |
| `GET`, `POST /rest/api/2/issue/{key}/transitions` | List statuses or move to one. |
| `GET`, `POST /rest/api/2/issue/{key}/comment` | List or add comments. |
| `POST /rest/api/2/issue/{key}/attachments` | Upload attachments. |
| `GET`, `DELETE /rest/api/2/attachment/{id}` | Read an attachment's metadata or delete it. |
| `GET /secure/attachment/{id}/{filename}`, `GET /rest/api/2/attachment/content/{id}` | Download an attachment's contents. |
| `GET /rest/api/2/issue/createmeta/{project}/issuetypes[/{type}]` | Create metadata. |
| `GET /rest/agile/1.0/board`, `GET /rest/agile/1.0/board/{id}/configuration` | One board per project, with a column per status. |

An issue's `renderedFields.description` is its Markdown rendered to HTML by goldmark, with raw HTML dropped.
Orca finds the attachments to download in it.

### Authentication

API routes accept an API key as `Authorization: Bearer <key>`, which is what Orca sends for a Server or Data Center site with a token and no username.
They also accept HTTP Basic with the principal's username and an API key as the password, for tools that only speak Basic.
They don't accept session cookies, so a page on another site can't make requests to the API with a signed-in user's cookie.
The one exception is attachment downloads, which also accept a session so the `/browse` page can show images.

Uploads require the `X-Atlassian-Token: no-check` header, as Jira does, which a cross-site form can't send.

A key limited to some projects sees only those projects in every response, and a read-only key gets HTTP 403 from every route that isn't a `GET`, apart from search.

To connect Orca, a user opens Settings, then Integrations, then Jira, and enters the site URL, the Server / Data Center deployment, no username, and an API key as the token.

### Attachment downloads

Downloads send `X-Content-Type-Options: nosniff` and `Content-Security-Policy: sandbox`.
PNG, JPEG, GIF, and WebP images are served inline.
Everything else, SVG included, is served with `Content-Disposition: attachment`, so an uploaded HTML or SVG file can't run script on the server's origin.

### JQL

JQL supports these operators and functions.

- `AND`, `OR`, and `NOT`.
- `=`, `!=`, `~`, `IN (...)`, and `IS EMPTY`.
- `currentUser()` and relative dates such as `-7d`.
- `ORDER BY`.

`currentUser()` is the calling principal, and `project = OPS` means the project the caller sees as `OPS`.

The server loads the issues the caller can see and evaluates the query in Go.
`alecthomas/participle` parses the query.

### Created-issue workaround

After creating an issue, Orca opens its Tasks pane on it, and the pane's close button then does nothing.
The create path only opens the pane when its follow-up detail read, the one with `expand=renderedFields`, returns the issue.
The server fails that read for a few seconds after each create, which keeps the pane closed.
Remove this once Orca fixes the bug or lets plugins add task sources (stablyai/orca#26282).

The window is in memory, so with more than one replica a detail read can reach a replica that didn't see the create.
The chart runs one replica.

## CLI

The task commands are clients of the server's Jira-compatible API, so the server holds all the access rules and there is one API to maintain.

```sh
export ORCA_TASKS_URL=https://tasks.example.com
export ORCA_TASKS_TOKEN=otk_...

orca-tasks catalog
orca-tasks project list
orca-tasks task list 'project = OPS AND statusCategory != Done ORDER BY updated DESC'
orca-tasks task show OPS-12
orca-tasks task create OPS "Rotate the API keys" --type Task --priority High --label security
orca-tasks task edit OPS-12 --summary "..." --add-label blocked --remove-label security
orca-tasks task move OPS-12 "In Progress" --comment "Starting on the key rotation."
orca-tasks task comment OPS-12 -
orca-tasks task attach OPS-12 diagram.png
```

`catalog` lists the projects, statuses, issue types, priorities, and users the other commands accept.
`task list` takes JQL.
`task show` prints the fields, the Markdown description, the comments, and the attachments.
`task create` prints the new key.
`task edit --priority ""` clears the priority, and `--assignee ""` unassigns.
`task attach` uploads a file and prints the Markdown that embeds it, to paste into a description or comment.
Names of projects, statuses, types, and priorities are case-insensitive.
An unknown one exits with status 2 and lists the valid choices.

Output is a table by default and JSON with `-f json`.
`-` in place of a description or comment reads it from stdin.

Projects are created and managed in the web interface, not the CLI.
An agent's actions are attributed to it because the CLI uses one of its API keys.

The server-side commands are `orca-tasks serve`, which also applies migrations, and `orca-tasks admin reset-link`.

## Claude Code skill

The repository ships the skill as a Claude Code plugin, so `/plugin marketplace add leightweight/orca-tasks` installs it.
The skill tells the agent to use the commands above, look before it writes, move tasks through their statuses with a comment, and leave other principals' tasks alone unless asked.

## Storage

Postgres 16 or later.
goose applies migrations at startup, under its own advisory lock, so several replicas starting at once apply each migration once.
Migrations only go forward; there are no down migrations.
sqlc generates the query code from `.sql` files, and pgx runs it.

Tables:

- `principals`: users and agents, with `owner_id` set for agents, the password hash, encrypted TOTP secret, and last TOTP step for users, `site_admin`, `disabled_at`, and sign-in times.
- `recovery_codes`: the code hash, user, and when it was used.
- `projects`, with `id`, `key`, and `next_number`.
- `memberships`: the project, the principal, the role, the user's key for the project, and `removed_at`.
  It is unique on project and principal, and on principal and key among active rows.
- `statuses`, `issue_types`, and `priorities`, seeded by a migration.
- `issues`, unique on project ID and number, with labels as `text[]`, and the reporter and assignee as memberships.
- `comments` and `attachments`, with the author or uploader as a membership.
  The `attachments` table holds metadata only.
- `issue_history`: one row per changed field.
- `api_keys`: the key hash, name, principal, access, expiry, revocation, and last use.
- `api_key_projects`: the projects a limited key can reach.
- `reset_links`: the token hash, user, and expiry.
- `sessions`: owned by `scs/pgxstore`.

Creating an issue increments the project's `next_number` with `UPDATE ... RETURNING` in the same transaction as the insert.

Attachment contents live in the data directory, so backups must cover both the database and that directory.

## Configuration

The server reads its configuration from flags or environment variables.

| Variable | Default | Purpose |
| --- | --- | --- |
| `ORCA_TASKS_DATABASE_URL` | Required | Postgres connection URL. |
| `ORCA_TASKS_BASE_URL` | Required | The URL users and Orca reach the server at, for links, TOTP labels, and secure cookies. |
| `ORCA_TASKS_SECRET_KEY` | Required | 32 random bytes in base64 that encrypt TOTP secrets. |
| `ORCA_TASKS_DATA_DIR` | `/data` | Directory holding attachment contents. |
| `ORCA_TASKS_MAX_UPLOAD` | `25MiB` | Largest attachment accepted. |
| `ORCA_TASKS_TRUSTED_PROXIES` | None | Proxies whose forwarding headers to trust, as comma-separated IPs and CIDRs, or `private` for the private, link-local, and unique local ranges. |
| `ORCA_TASKS_ADDR` | `:8080` | Address to listen on. |
| `ORCA_TASKS_LOG_LEVEL` | `info` | Log level. |
| `ORCA_TASKS_LOG_FORMAT` | `json` | Log format, `json` or `text`. |

Losing the secret key makes every TOTP secret unreadable, so each user then needs a reset link.

### Client addresses

The server uses the client's address for sign-in limits and logs.
It always trusts loopback, plus the ranges in `ORCA_TASKS_TRUSTED_PROXIES`.
When the connecting address is trusted, the client is the rightmost `X-Forwarded-For` hop that isn't trusted.
Otherwise the client is the connecting address, and the server ignores the forwarding headers.
`realclientip-go`'s rightmost trusted range strategy implements this.

The server builds links and decides whether cookies are `Secure` from `ORCA_TASKS_BASE_URL`, so it doesn't read `X-Forwarded-Proto` or `X-Forwarded-Host`.

`/healthz` reports that the process is up.
`/readyz` also checks the database and that the data directory is writable.

## Implementation

| Job | Library |
| --- | --- |
| Flags, environment, help | `github.com/alecthomas/kong` |
| Logging | `log/slog` |
| HTTP routing | `net/http` |
| Postgres | `github.com/jackc/pgx/v5` |
| Query code | `sqlc` |
| Migrations | `github.com/pressly/goose/v3` |
| Sessions | `github.com/alexedwards/scs/v2` with `pgxstore` |
| Password hashing | `github.com/alexedwards/argon2id` |
| TOTP | `github.com/pquerna/otp` |
| Encrypting TOTP secrets | `golang.org/x/crypto/nacl/secretbox` |
| Sign-in limits | `github.com/sethvargo/go-limiter` |
| Client addresses behind proxies | `github.com/realclientip/realclientip-go` |
| JQL parsing | `github.com/alecthomas/participle/v2` |
| Markdown to HTML | `github.com/yuin/goldmark` |
| CLI tables | `github.com/jedib0t/go-pretty/v6` |
| Human-readable sizes and times | `github.com/dustin/go-humanize` |
| SQL formatting | steveql-fmt |
| Tests against Postgres | `github.com/testcontainers/testcontainers-go` |

## Distribution

### Releases

goreleaser builds releases from `v*` tags.
A release publishes the following.

- A multi-platform image for linux/amd64 and linux/arm64 on a distroless nonroot base, signed with cosign.
- CLI archives for macOS and Linux on amd64 and arm64, so `mise use github:leightweight/orca-tasks` installs the CLI.
- The Helm chart, signed with cosign.

### Helm chart

The chart runs one replica as a Deployment with a Service, a PersistentVolumeClaim for the data directory, and an optional Ingress.
The Deployment uses the Recreate strategy, since the volume is ReadWriteOnce.
It doesn't run Postgres.
It reads the database URL and the secret key from existing Secrets, so a CloudNativePG cluster's generated app Secret works through its `uri` key.

## CI

GitHub Actions runs `mise run lint` and `mise run test` on pull requests and on `main`.
Renovate opens dependency updates weekly.

## Deferred

- A task board in the web interface, including deleting issues and showing their history.
- Exporting a server's data and attachments, for backups and moving servers.
- Custom statuses, types, and priorities per project.
- Email, including emailed password resets.
- Single sign-on through OIDC.
- WebAuthn and passkeys as a second factor.
- Watchers, issue links, and editing or deleting comments.
- Saved filters.
- Translating JQL to SQL.

## Open risks

- With `ORCA_TASKS_TRUSTED_PROXIES` unset behind an ingress, every client shares the ingress's address, so one client's failed sign-ins lock out everyone.
  The `/admin/proxy` page shows whether the setting is right.
- Orca is the compatibility target, and it can start calling a route or reading a field we don't implement.
  The server logs requests to unknown routes so those show up.
