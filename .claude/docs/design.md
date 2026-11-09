# orca-tasks design

## Summary

orca-tasks is a self-hosted task tracker.
It stores tasks in Postgres and serves them through a subset of the Jira Server REST API, so Orca's Tasks page can use it as a Jira site.
A CLI calls the same API, so Claude Code agents can read and change the same tasks through a skill.

Several people share one server.
Each has their own login, agents, and API keys, and sees only the projects they belong to.

The web interface manages accounts, projects, and access, and lists each project's issues read-only.
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

A principal is anything that can authenticate and act on tasks: report an issue, comment, or upload an attachment.
There are two kinds.

- A user is a person.
  Users sign in to the web interface with a username, a password, and a TOTP code.
- An agent belongs to one user, such as `claude` acting for that user.
  Agents can't sign in to the web interface.
  They authenticate only with API keys.

A username starts with a lowercase letter and holds only lowercase letters, digits, `.`, `_`, and `-`.
An agent's name follows the same rule, but only has to be unique among its owner's agents.
Its username is the owner's and its name joined by `/`, as in `alice/claude`.
Jira identifies people by name alone, so the API and JQL use that full username, and because usernames can't contain `/`, it never collides with a user's.
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
A project always keeps an administrator: the last active one can't leave or become a member until someone else is an administrator.
A lock on the project makes two administrators leaving at once count each other correctly.

### Choosing people and recording actions

People are chosen, and actions are recorded.

- Wherever someone picks a person, the choice is a user: an issue's assignee, Orca's assignee picker, user search, and project members.
  Agents never appear there, and the API refuses to assign one.
- Wherever something records who acted, it may be a user or an agent: an issue's reporter, a comment's author, an attachment's uploader, and history entries.
- An agent acts for its owner.
  Assigning to `-1`, Jira's automatic assignee, assigns the caller's user, so an agent assigns its owner.
  Wherever a permission depends on who did something, such as deleting an attachment, the owner and their agents count as one, and an agent has its owner's role.

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
The user can change the key for any of their memberships on the project's page, to replace that suffix with something clearer.

An issue's key is the viewer's key for its project followed by the issue number, such as `OPS-12` for one member and `OPS2-12` for another.
The API, JQL, the CLI, and `/browse` links all use the caller's keys.
The site administration pages show the project's own key and its ID.

Two members with different keys for a project see different keys for the same issue, so a key written in a comment means what it meant to its author.
Changing a membership key also changes that user's issue keys, so Orca worktrees linked to the old keys lose their link.

### Issues and comments

An issue has a summary, a Markdown description, a status, a type, an optional priority, an optional assignee, an optional parent, a reporter, labels, and created and updated times.
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

### Parents

An issue's parent is another issue in the same project, such as the epic a task belongs to.
Any type can be a parent, and parents nest to any depth.
An issue can't be its own parent or the parent of an issue above it.
The server checks for cycles under a lock on the project, so two edits at once can't make one between them.
Its history entries record the parent's issue number, which is the same for every member, unlike its key.

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
- A new assignee must be a user with an active membership in the issue's project.
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

On a shared server, every user must use TOTP, including the first administrator.
A user has no way to turn it off.
[Local mode](#local-mode) is the one exception: it signs in with a password alone.

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
An agent has a name, such as `claude`, which makes its username `alice/claude`, and a display name, such as `Claude`.
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

The web interface is server-rendered `html/template` pages embedded in the binary.
[Oat](https://oat.ink) styles the pages, and its small script positions menus and opens the sidebar on phones; `internal/web/static/style.css` adds the layout and what Oat doesn't cover.
The binary embeds a pinned copy of Oat in `internal/web/static/oat/`, so pages load nothing from another site and work in local mode without a network.
`mise run oat:update [version]` replaces it.
Every page works with scripts blocked, since menus are native popovers and forms post and redirect.

Pages are organized around projects.
A sidebar has a project selector, then Tasks, Members, and Settings for the selected project, and, for site administrators, the administration pages at its foot.
The selected project is the one the page belongs to, or the one the user opened last, and `/` opens that project's tasks.
A profile menu at the top right has Profile, API keys, Agents, and Sign out, and those three account pages share tabs.

Times read as relative, such as "4 days ago", and show the exact time in UTC on hover, since the server doesn't know the viewer's time zone.
Revoking a key, removing or leaving a member, disabling an agent or user, and changing site administration link to a page that says what will happen, and its button posts the change.

| Page | Who | Purpose |
| --- | --- | --- |
| `/setup` | Anyone, before the first user exists | Create the first administrator. |
| `/login`, `/logout` | Users | Sign in and out. |
| `/` | Users | Open the last project's tasks, or the project list. |
| `/account` | Users | Change display name, email, and password, and generate recovery codes. |
| `/account/keys` | Users | Create and revoke the user's API keys. |
| `/account/agents` | Users | Create and manage agents and their API keys. |
| `/projects` | Users | List the user's projects and create a project. |
| `/projects/{key}/tasks` | Project members | List the project's open or all tasks. `/projects/{key}` leads here. |
| `/projects/{key}/members` | Project members | List members, and let administrators add and remove them and change roles. |
| `/projects/{key}/settings` | Project members | Change the key the user sees the project under, and let administrators rename and delete it. |
| `/admin/users` | Site administrators | List users with their counts and create users. |
| `/admin/users/{id}` | Site administrators | Show one user's projects and counts, disable or enable them, grant administration, and generate a reset link. |
| `/admin/projects` | Site administrators | List every project with counts and members. |
| `/admin/projects/{id}` | Site administrators | Show one project's members, add members, and delete the project. |
| `/admin/proxy` | Site administrators | Show the connecting address, the forwarding headers, which hops are trusted, and the client address the server resolved. |
| `/browse/{key}` | Project members | Show one task with its parent, description, child tasks, comments, and attachments, read-only, under its project in the sidebar. |

Orca links each issue to `{site}/browse/{KEY}`, which is why the read-only issue page exists.

For each user, the user's page shows their projects, agents, active API keys, issues reported, issues assigned, comments, and attachments.
It also shows the bytes of description, comment, and attachment data from the user and their agents, the last sign-in, and the last API key use.
The user list shows the main counts and when each user was last seen, by sign-in or key use.

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

An issue's `parent` field has the parent's ID, key, summary, status, and type, as Jira Cloud sends it.
Orca shows the key, summary, and type.
Creating or editing an issue sets the parent from `{"key": ...}` or `{"id": ...}`, and editing it to `null` removes it.
A parent the caller can't see, or one in another project, gets HTTP 400.

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
- `EMPTY` and `NULL`, unquoted, mean no value with `=`, `!=`, `IN`, and `NOT IN`; `IS` and `IS NOT` take only those.
- `currentUser()` and relative dates such as `-7d`.
- `ORDER BY`.

`currentUser()` is the calling principal, and for an agent, its owner too, so an agent's `assignee = currentUser()` finds its owner's issues.
A field outside the ones issues have is an error, so a typo doesn't match every issue.
`sprint` is known but has no values, so Orca's `sprint in openSprints()` matches nothing.
`issue` is another name for `key`, and `id` is the issue's numeric ID.
`project = OPS` means the project the caller sees as `OPS`.
`parent = OPS-3` finds the issues under OPS-3, and `parent IS EMPTY` the ones without a parent.

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
orca-tasks task create OPS "Rotate the API keys" --type Task --priority High --label security --parent OPS-3
orca-tasks task edit OPS-12 --summary "..." --add-label blocked --remove-label security
orca-tasks task move OPS-12 "In Progress" --comment "Starting on the key rotation."
orca-tasks task comment OPS-12 -
orca-tasks task attach OPS-12 diagram.png
```

`catalog` lists the projects, statuses, issue types, priorities, and users the other commands accept.
`task list` takes JQL.
`task show` prints the fields, the Markdown description, the child tasks, the comments, and the attachments.
`task create`, `task edit`, and `task move` print the task: a table row, or with `-f json` one object with the same fields as a `task list` row.
`task edit --priority ""` clears the priority, `--assignee ""` unassigns, and `--parent ""` removes the parent.
`--parent` on `create` and `edit` takes the key of an issue in the same project.
`--assignee` takes a user.
`--assign-to-me` on `create`, `edit`, and `move` assigns the caller's user, which with an agent's key is the agent's owner.
`task attach` uploads a file and prints the Markdown that embeds it, to paste into a description or comment.
Names of projects, statuses, types, and priorities are case-insensitive.
An unknown one exits with status 2 and lists the valid choices.

Output is a table by default and JSON with `-f json`.
`-` in place of a description or comment reads it from stdin.

Projects are created and managed in the web interface, not the CLI.
An agent's actions are attributed to it because the CLI uses one of its API keys.

The server-side commands are `orca-tasks serve`, which also applies migrations, and `orca-tasks admin reset-link`.

## Importing

`orca-tasks admin import` brings projects, issues, and comments in from another task list.
Like `admin reset-link`, it connects to the database directly, so it runs inside the server's container.

```sh
kubectl exec -i deploy/orca-tasks -- /orca-tasks admin import --owner alice - < export.json
```

The file is JSON, with `format` set to `orca-tasks-export` and `version` set to 1.

```json
{
  "format": "orca-tasks-export",
  "version": 1,
  "projects": [
    {
      "key": "OPS",
      "name": "Operations",
      "next_number": 13,
      "issues": [
        {
          "number": 12,
          "summary": "Rotate the API keys",
          "description": "Markdown.",
          "status": "In Progress",
          "type": "Task",
          "priority": "High",
          "assignee": "",
          "reporter": "claude",
          "labels": ["security"],
          "created": "2026-10-22T13:43:53Z",
          "updated": "2026-10-23T09:12:00Z",
          "comments": [
            {"author": "claude", "body": "Started.", "created": "2026-10-22T14:00:00Z", "updated": "2026-10-22T14:00:00Z"}
          ]
        }
      ]
    }
  ]
}
```

- `--owner` names the user who administers every imported project.
  An issue or comment with an empty reporter or author is theirs.
- People in the file are usernames.
  `--user OLD=NEW` maps one to a user or agent here.
  A name it doesn't map is looked up as a user, then as one of the owner's agents, so an export's `claude` finds `alice/claude` with no mapping.
- Every user the file names joins the project as a member, and every agent joins with its owner.
- An issue assigned to an agent goes to the agent's owner.
- Issues keep their numbers and times, and comments keep their authors and times, so `OPS-12` is still `OPS-12` for the owner.
  `next_number` keeps the counter past numbers the old list used up.
- Statuses, types, and priorities match the catalog by name, ignoring case.
- The import runs in one transaction.
  Before it writes anything, it checks the whole file and reports all the problems together: unknown names, statuses, types, and priorities, duplicate project keys, issue numbers that aren't positive or repeat within a project, empty summaries, and missing times or updates dated before their creation.
  It refuses a project whose key the owner already uses, so running it twice imports nothing the second time.
- `--dry-run` runs the whole import and rolls it back.
- Each imported issue gets an `imported` history entry.
- Attachments and saved filters aren't part of the format.

Each project costs a fixed number of queries whatever its size: one COPY for its issues, one query for their IDs, and one COPY for its comments.

## Claude Code skill

The repository ships the skill as a Claude Code plugin, so `/plugin marketplace add leightweight/orca-tasks` installs it.
The skill tells the agent to use the commands above, look before it writes, move tasks through their statuses with a comment, leave assigning to the user except to assign the user a task it starts, and leave other people's tasks alone unless asked.

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
- `issues`, unique on project ID and number, with labels as `text[]`, the reporter and assignee as memberships, and `parent_id`.
  A composite foreign key on the project ID keeps a parent in its child's project.
- `comments` and `attachments`, with the author or uploader as a membership.
  The `attachments` table holds metadata only.
- `issue_history`: one row per changed field.
- `api_keys`: the key hash, name, principal, access, expiry, revocation, and last use.
- `api_key_projects`: the projects a limited key can reach.
- `reset_links`: the token hash, user, and expiry.
- `sessions`: owned by `scs/pgxstore`.

Creating an issue increments the project's `next_number` with `UPDATE ... RETURNING` in the same transaction as the insert.

Attachment contents live in the data directory, so backups must cover both the database and that directory.

## Local mode

`orca-tasks serve --local` runs the server on one person's machine, for someone who uses Orca alone and has no server to host it on.
It needs no Docker and no configuration.

```sh
mise use -g packslip:github.com/leightweight/orca-tasks
orca-tasks serve --local
```

- Files go where the XDG Base Directory specification says, each in an `orca-tasks` directory with mode 0700:
  - Data worth keeping, which is the database, its password, attachments, and the secret key, goes in `$XDG_DATA_HOME`, which defaults to `~/.local/share`.
  - The downloaded Postgres goes in `$XDG_CACHE_HOME`, which defaults to `~/.cache`.
    Deleting it costs a download.
  - The agent socket and Postgres's runtime files go in `$XDG_RUNTIME_DIR`.
    macOS doesn't set it, so the fallback is `orca-tasks-<uid>` in the temporary directory, which is private to the user on macOS.
    A runtime directory that already exists must belong to the user and be private, since another account could have made one in a shared temporary directory to read the socket.
  - Relative values are ignored, as the specification says.
  - `$ORCA_TASKS_LOCAL_DIR` puts everything under one directory instead, with the cache in `cache` and runtime files in `run`.
    It must be absolute: a relative one would depend on where each command started.
- Postgres runs embedded, through `fergusstrange/embedded-postgres`, which downloads a Postgres 18 build the first time and runs it as a child process.
  It listens on 127.0.0.1, on a free port each run, with a password generated once and kept in the data directory.
- The secret key is generated the first time and kept in the data directory, so there's nothing to configure.
- The server listens on `127.0.0.1:8080`, and its base URL is `http://127.0.0.1:8080`, unless `--addr` or `--base-url` say otherwise.
  `--addr` must be a loopback address: local mode refuses to listen where another machine could reach it.
  Logs are text rather than JSON.
- Setup and sign-in take a password alone, with no TOTP or recovery codes, and creating an API key asks for no code.
  The password is what protects a local server.
  Websites the person visits can send requests to 127.0.0.1, but can't sign in without the password or use the person's session, whose cookie belongs to 127.0.0.1.
  Other accounts on the machine can reach the port, but not without the password.
  Software running as the person could read the database files anyway.
  TOTP would only matter if the password leaked to someone on another machine, and local mode never listens where another machine can reach it.
  Skipping sign-in entirely would let any website make requests from inside the browser, so the password stays.
- Setup also creates an agent named `agent`.

### The agent socket

A local server also serves the API on a Unix socket, `agent.sock` in the runtime directory, with mode 0600.
macOS limits socket paths to 104 bytes, so the server refuses a runtime directory whose socket path would be longer, and says to set `XDG_RUNTIME_DIR` or `ORCA_TASKS_LOCAL_DIR`.
Requests through it act as the first user's `agent` agent, with no API key.
The file mode limits it to the machine's owner, the same people who could read a key from their own environment.

The CLI uses `ORCA_TASKS_URL` and `ORCA_TASKS_TOKEN` when they're set.
Without them, it uses the local socket, so a Claude Code session on the same machine works with no credentials at all.
The server also records the socket's path in `socket-path` in the data directory.
The CLI tries the socket its own environment points to, then the recorded one, so a command whose `TMPDIR` or `XDG_RUNTIME_DIR` differs from the server's, as from cron or a sandboxed shell, still finds it.
If none answers, the error lists every path it tried.
`ORCA_TASKS_SOCKET` or `--socket` names another socket.

### Keeping the data safe

- embedded-postgres never erases or upgrades a data directory, and refuses to start against one made by another Postgres major version, so a version bump can never wipe someone's tasks.
  Moving to a new Postgres version would need its own migration of the data directory.
- A server holds an exclusive lock on `lock` in the data directory for as long as it runs, and a second `serve --local` with the same data directory refuses to start.
  The lock belongs to the data directory, not the socket, so it holds even when the two were started with different runtime directories.
- embedded-postgres runs Postgres under a supervisor process that stops it when the server exits, even from `kill -9`, so a killed server doesn't leave its database running.
- Ctrl-C stops Postgres and removes the socket.

### Admin commands

`orca-tasks admin reset-link --local USER` and `orca-tasks admin import --local` work on the local database, for someone who forgets their password or moves tasks in.
They start the embedded Postgres themselves, so the server must be stopped; while it holds the data directory's lock, they say so.
`reset-link --local` prints a link to `http://127.0.0.1:8080` unless `--base-url` says otherwise.

Backing up is copying the data directory while the server is stopped.

## Configuration

The server reads its configuration from flags or environment variables.

| Variable | Default | Purpose |
| --- | --- | --- |
| `ORCA_TASKS_DATABASE_URL` | Required, except with `--local` | Postgres connection URL. |
| `ORCA_TASKS_BASE_URL` | Required, except with `--local` | The URL users and Orca reach the server at, for links, TOTP labels, and secure cookies. |
| `ORCA_TASKS_SECRET_KEY` | Required, except with `--local` | 32 random bytes in base64 that encrypt TOTP secrets. |
| `ORCA_TASKS_DATA_DIR` | `/data`, or `attachments` in the XDG data directory with `--local` | Directory holding attachment contents. |
| `ORCA_TASKS_MAX_UPLOAD` | `25MiB` | Largest attachment accepted. |
| `ORCA_TASKS_TRUSTED_PROXIES` | None | Proxies whose forwarding headers to trust, as comma-separated IPs and CIDRs, or `private` for the private, link-local, and unique local ranges. |
| `ORCA_TASKS_ADDR` | `:8080`, or `127.0.0.1:8080` with `--local` | Address to listen on. |
| `ORCA_TASKS_LOG_LEVEL` | `info` | Log level. |
| `ORCA_TASKS_LOG_FORMAT` | `json`, or `text` with `--local` | Log format, `json` or `text`. |
| `ORCA_TASKS_LOCAL_DIR` | The XDG directories | One directory for everything `--local` keeps, instead of the XDG data, cache, and runtime directories. |

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
| Web interface styles | Oat (`@knadh/oat`) |
| CLI tables | `github.com/jedib0t/go-pretty/v6` |
| Human-readable sizes and times | `github.com/dustin/go-humanize` |
| SQL formatting | steveql-fmt |
| Tests against Postgres | `github.com/testcontainers/testcontainers-go` |
| Postgres in local mode | `github.com/fergusstrange/embedded-postgres` |

## Distribution

### Releases

goreleaser builds releases from `v*` tags.
A release publishes the following.

- A multi-platform image for linux/amd64 and linux/arm64 on a distroless nonroot base, signed with cosign.
- CLI archives for macOS and Linux on amd64 and arm64.
- A [packslip](https://packslip.dev) for the archives, signed with the workflow's identity, so `mise use packslip:github.com/leightweight/orca-tasks` installs the CLI and verifies it.
  A job that can't change the release signs it, and a separate job uploads it.
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
- Watchers, issue links other than parents, and editing or deleting comments.
- Saved filters.
- Translating JQL to SQL.

## Open risks

- With `ORCA_TASKS_TRUSTED_PROXIES` unset behind an ingress, every client shares the ingress's address, so one client's failed sign-ins lock out everyone.
  The `/admin/proxy` page shows whether the setting is right.
- Orca is the compatibility target, and it can start calling a route or reading a field we don't implement.
  The server logs requests to unknown routes so those show up.
