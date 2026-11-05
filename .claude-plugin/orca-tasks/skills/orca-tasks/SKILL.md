---
name: orca-tasks
description: Track your own work items in the user's orca-tasks server, which Orca shows on its Tasks page as a Jira site. Use when asked to create, update, move, comment on, or look up tasks or issues with keys like OPS-12, or to keep a task list for the work in this session.
---

# orca-tasks

`orca-tasks` is a command on the PATH that edits the user's task list on an orca-tasks server.
Orca shows the same list on its Tasks page, so anything you change there is what the user sees.

It reads the server's URL from `ORCA_TASKS_URL` and an API key from `ORCA_TASKS_TOKEN`.
Without them, it uses a server the user runs on this machine with `orca-tasks serve --local`, which needs no key.
Either way, you act as an agent the user made for you, so the tasks you create and your comments show under the agent's name.
You act for that user: tasks are assigned to people, never to agents.
If a command says there's no server to talk to, or fails with a 403, stop and ask the user to set it up.
Don't ask for the key in the conversation, and never print it.

## Look before you write

```sh
orca-tasks catalog                    # projects, statuses, types, priorities, users
orca-tasks task list 'project = OPS AND statusCategory != Done ORDER BY updated DESC'
orca-tasks task show OPS-12 -f json  # fields, Markdown description, comments, and attachments
```

`task list` takes JQL: `AND`, `OR`, `NOT`, `=`, `!=`, `~` (contains), `IN (...)`, `IS EMPTY`, `currentUser()`, `-7d`-style dates, and `ORDER BY`.
`currentUser()` matches you and the user you work for, so `assignee = currentUser()` finds the user's tasks.
Add `-f json` to any command for output to parse.
Don't create projects; they're managed in the server's web interface.
Ask the user which project to use if `catalog` doesn't make it obvious.

## Change things

```sh
orca-tasks task create OPS "Rotate the API keys" --type Task --priority High --label security
orca-tasks task edit OPS-12 --summary "..." --add-label blocked --remove-label security
orca-tasks task move OPS-12 "In Progress" --assign-to-me --comment "Starting on the key rotation."
orca-tasks task comment OPS-12 "Rotated staging. Production is next."
orca-tasks task attach OPS-12 diagram.png
orca-tasks task move OPS-12 Done --comment "All environments rotated; old keys revoked."
```

- Descriptions and comments are Markdown.
  Pass `-` to read one from stdin, as in `orca-tasks task comment OPS-12 - <<'EOF'`, which is the way to send anything longer than a line.
- `--description` on `edit` replaces the whole body.
  Run `show` first if you mean to add to it.
- Don't assign tasks when you create them, and don't reassign anyone's; leave that to the user.
  When you start an unassigned task, `--assign-to-me` assigns it to the user you work for.
- `edit --priority ""` clears the priority.
- `attach` prints the Markdown that shows the file.
  Put it in a description or comment, and Orca shows images inline.
- Names are case-insensitive.
  An unknown status, type, priority, or project exits 2 with the valid choices listed.
- `create`, `edit`, and `move` print the task, with `-f json` as one object with the same fields as a `task list` row.
  Read the new key from `create`, as in `orca-tasks task create OPS "..." -f json | jq -r .key`, rather than guessing the number.

Move a task to `In Progress` when you start it and to `Done` when it's finished, with a comment saying what was done.
Leave tasks assigned to other people alone unless asked.
