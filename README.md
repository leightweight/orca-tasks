# orca-tasks

orca-tasks is a self-hosted task tracker that serves a Jira-compatible API, so [Orca](https://github.com/stablyai/orca)'s Tasks page can use it as a Jira site.
A CLI calls the same API, so Claude Code agents can read and change the same tasks through a skill.

- Several people share one server, and each sees only the projects they belong to.
- Every account signs in with a password and TOTP.
- Agents, such as Claude, get their own names and API keys, so the tasks they create and their comments show as theirs.
  Tasks are assigned to people, not agents.
- API keys can be limited to reading, or to some projects.
- Attachments are stored as files, and Orca shows attached images inline.
- Every change to an issue is recorded.

The web interface manages accounts, projects, and access.
It isn't a task board: people work with tasks in Orca, and agents work with them through the CLI.

## Run it on your own machine

If nobody hosts a server for you, run one in a terminal:

```sh
mise use -g packslip:github.com/leightweight/orca-tasks
orca-tasks serve --local
```

It needs no Docker and no configuration.
The first run downloads Postgres.
Files go in the XDG directories: your tasks, attachments, and a generated secret key in `~/.local/share/orca-tasks`, the Postgres download in `~/.cache/orca-tasks`, and the socket in `$XDG_RUNTIME_DIR`, or the temporary directory where that isn't set.
Open <http://127.0.0.1:8080/setup>, enter the setup code from the terminal, and create your account with a password.
Local mode only listens on this machine, so it signs in with a password alone, with no authenticator app.
Setup also creates an agent named `agent`.

Connect Orca as below, with `http://127.0.0.1:8080` as the site and an API key from the API keys page.
Claude Code needs no key at all: when `ORCA_TASKS_URL` isn't set, the CLI talks to the local server through a socket only you can open, as your `agent`.
Install the skill as in [Set up Claude Code](#set-up-claude-code) and skip the environment variables.

If you forget your password, stop the server, run `orca-tasks admin reset-link --local YOUR-USERNAME`, start the server again, and open the link it printed.
`orca-tasks admin import --local` imports tasks the same way, with the server stopped.
To back up, stop the server and copy `~/.local/share/orca-tasks`.

## Install

orca-tasks needs Postgres 16 or later, which the chart doesn't run.
A [CloudNativePG](https://cloudnative-pg.io) cluster works, and its generated app Secret has the connection URL under the key `uri`.

Create a Secret with a key that encrypts TOTP secrets, and keep a copy of it somewhere safe:

```sh
kubectl create secret generic orca-tasks-secret-key --from-literal=secret-key="$(openssl rand -base64 32)"
```

Install the chart:

```sh
helm install orca-tasks oci://ghcr.io/leightweight/charts/orca-tasks \
  --set baseURL=https://tasks.example.com \
  --set database.existingSecret=orca-tasks-db-app \
  --set secretKey.existingSecret=orca-tasks-secret-key \
  --set trustedProxies=private \
  --set ingress.enabled=true \
  --set 'ingress.hosts[0].host=tasks.example.com' \
  --set 'ingress.hosts[0].paths[0].path=/' \
  --set 'ingress.hosts[0].paths[0].pathType=Prefix'
```

[`values.yaml`](charts/orca-tasks/values.yaml) lists every setting.
Set `trustedProxies` to your ingress's addresses so failed sign-ins count per client.
The `/admin/proxy` page shows whether it's right.

On a new database, the server logs a setup code:

```sh
kubectl logs deploy/orca-tasks | grep setup_code
```

Open `https://tasks.example.com/setup`, enter the code, and create the first account, which becomes the site administrator.

## Import tasks

`orca-tasks admin import` brings in projects, issues, and comments from a JSON export, keeping issue numbers, authors, and times.
Create your account first, and any agents the export's issues and comments came from, under the names the export uses, such as `claude`:

```sh
kubectl exec -i deploy/orca-tasks -- /orca-tasks admin import --owner you --dry-run - < export.json
kubectl exec -i deploy/orca-tasks -- /orca-tasks admin import --owner you - < export.json
```

The [design doc](.claude/docs/design.md#importing) describes the file format.

## Connect Orca

Create an API key on the API keys page.
In Orca, open Settings, then Integrations, then Jira, and enter:

- the server's URL as the site,
- Server / Data Center as the deployment,
- no username,
- the API key as the token.

## Set up Claude Code

Create an agent named `claude` on the Agents page, and an API key for it.
Its full username is yours and its name together, as in `you/claude`, so other people can name their agents `claude` too.
Install the CLI with mise:

```sh
mise use -g packslip:github.com/leightweight/orca-tasks
```

Each release publishes a [packslip](https://packslip.dev), a signed list of its archives and their digests.
mise checks that the release was signed by this repository's release workflow and that each download matches before it installs anything.
mise skips releases less than a day old when it resolves `latest`, so name the version, as in `@0.1.0`, to install one sooner.
The archives are also on the [releases](https://github.com/leightweight/orca-tasks/releases) page.

Give it the server and the agent's key:

```sh
export ORCA_TASKS_URL=https://tasks.example.com
export ORCA_TASKS_TOKEN=otk_...
```

Then install the skill:

```text
/plugin marketplace add leightweight/orca-tasks
/plugin install orca-tasks@orca-tasks
```

Claude then keeps its work in your task list:

```sh
orca-tasks task list 'assignee = currentUser() AND statusCategory != Done'
orca-tasks task move OPS-12 "In Progress" --comment "Starting on the key rotation."
```

## Locked out

If the only administrator loses their password and recovery codes, print a reset link from inside the server's container:

```sh
kubectl exec deploy/orca-tasks -- /orca-tasks admin reset-link USERNAME
```

## Try it locally

[`compose.yml`](compose.yml) runs the server and Postgres on this machine:

```sh
mise run up            # build the image and start both
mise run down          # stop them, keeping the data
mise run down --wipe   # stop them and delete the data
```

Open <http://localhost:8090/setup> and enter the setup code from the log.
The compose file uses a public secret key, so don't keep real tasks there.

## Development

The toolchain and tasks live in mise.
Tests start Postgres in a container, so they need Docker.

```sh
mise bootstrap      # install the git hooks
mise run test
mise run lint
mise tasks ls       # everything else
```

[`.claude/docs/design.md`](.claude/docs/design.md) is the design, and [`.claude/rules/`](.claude/rules/) has the project's conventions.

## License

MIT.
orca-tasks implements a Jira-compatible API; it isn't affiliated with or endorsed by Atlassian.
