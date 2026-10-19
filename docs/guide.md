# SaladCloud CLI guide

How to use the CLI, from setup to the contracts scripts rely on. For every command at a glance, see
[Commands](commands.md); for behavior and API quirks worth knowing, see [Notes](notes.md).

## Configuration

### Logging in

```sh
saladcloud login                 # Save your API key, organization, and project
saladcloud use my-org/default    # Switch to another organization and project later
saladcloud use my-org            # Same, with the organization's "default" project
```

Organization and project are always their **URL names**, the segments in
`https://portal.salad.com/organizations/<org>/projects/<project>`, not the
display names the portal shows (`saladcloud orgs list` shows both for
organizations; a project's URL name is in the portal URL). Every
organization starts with a project named `default`.

At a terminal, `login` asks for the key with the input hidden; pressing
Enter at that prompt opens `https://portal.salad.com/api-key` in your
browser (not over SSH, or on Linux without a display). It checks the key,
then picks the organization: `--org` or `SALAD_ORGANIZATION` when set,
otherwise your only organization, or a numbered list of them with the saved
one offered. The project is `--project`, `SALAD_PROJECT`, the one saved for
that organization, or `default`, offered in brackets. Container groups and
queues need a project; inference, transcription and the AI Gateway do not,
so when the organization has no `default` you can press Enter to skip it,
and login still saves the key and organization. Ctrl-C at any prompt stops
login and changes nothing.

Without a terminal login never prompts or picks for you. The key comes from
`--api-key -`, which reads the first line of stdin, from `--api-key`, or
from `SALAD_API_KEY` (checked and used, but not saved); the organization
from `--org`, `SALAD_ORGANIZATION` or the config file:

```sh
# bash or zsh
printf '%s' "$KEY" | saladcloud login --api-key - --org my-org

# PowerShell
$env:KEY | saladcloud login --api-key - --org my-org
```

Nothing is saved until the key, organization and project are settled, and
the config file is checked first, so a file that cannot be read or written
stops login before it asks for anything. `--api-key <key>` works too, with
a warning: a key typed on the command line is exposed. Stored keys are
shown masked to their last four characters (`****...ABCD`), in tables and in
JSON; a key of eight characters or fewer shows as `****` alone.

When a variable would override what login saves (`SALAD_API_KEY`, or
`SALAD_ORGANIZATION` or `SALAD_PROJECT` different from what you named),
login says so before it asks, and warns after saving; at a terminal the
warning includes the commands that remove the variable. `login -o json`
reports where commands will read the key from (`api_key_source`), whether
it was saved (`api_key_saved`), the saved key commands use where
`SALAD_API_KEY` is not set (`saved_api_key`, masked), whether the project
saved for the organization was found (`saved_project_found`: `false` when
login fell back, `null` when none was saved or you named one) and the
overriding variables (`env_overrides`).

### Without logging in

Scripts and CI set `SALAD_API_KEY`, `SALAD_ORGANIZATION`, `SALAD_PROJECT`
(or pass `--org`, and `--project` where a command takes it) and skip `login`
entirely; the AI Gateway key
is `SALAD_AI_GATEWAY_API_KEY`. Precedence per field:
flag > environment > config file. The project has two refinements. A saved
project belongs to the saved organization: with another organization from
`--org` or `SALAD_ORGANIZATION`, it is not used. And when nothing sets a
project, `default` is assumed — every organization is created with a
project of that name — and every command that uses it prints `warning:
using project "default" because none was set` on stderr (`whoami` shows
`none set, using "default"`). In an organization with several
projects, set `SALAD_PROJECT` so a script never acts on `default` by
accident. If the organization has no `default` project, the command exits
2 with "no valid project set": pass `--project` or set `SALAD_PROJECT`.
There is no command that lists projects; the names are the `<project>` in
`https://portal.salad.com/organizations/<org>/projects/<project>`, and a
project is created in the portal.

### Logging out

`saladcloud logout` removes the API key from the config file and says which
organization and project stay saved; `saladcloud ai-gateway logout` removes
the AI Gateway key. Both warn when an environment variable still supplies a
key (`env_overrides` in JSON).

`saladcloud whoami` shows the API key (masked), organization, project and AI
Gateway key in use, each with where it came from; a value from a variable or
flag that hides a different saved one reads `(from SALAD_API_KEY; saved:
****...9QxZ)`, and the JSON carries every saved value as `saved_*`.
`authenticated` says whether the API accepted the key, `organization_found`
whether the organization exists for it, and `organizations` lists yours.
whoami reports and never fails over a missing or rejected key or an
organization that is not found; a network error is still an error.

### The config file

The config file lives at the platform's user config directory, written
`0600` on Unix (Windows ACLs are not adjusted): `%AppData%\saladcloud\config.json`
on Windows, `~/Library/Application Support/saladcloud/config.json` on macOS,
`$XDG_CONFIG_HOME/saladcloud/config.json` (default `~/.config/…`) on Linux.
`saladcloud config path` prints it; `SALAD_CLI_CONFIG_DIR` overrides the
directory (useful for tests and sandboxes). A corrupt file blocks every
command that reads it, including `config set` — fix or delete it (`config path`
still prints where it is).

The keys are `api-key`, `organization`, `project` and `ai-gateway-api-key`.
`config set api-key -` (or `ai-gateway-api-key -`) reads the value from
the first line of stdin, so the key is not exposed on the command line.
`config set` writes the value as given, with no check; `saladcloud use
<org>[/<project>]` switches the organization and project together, checked
against the API, and with `<org>` alone picks its `default` project, or
saves no project when the organization has none. `use`, `config set` and
`config unset` warn when a variable (`SALAD_ORGANIZATION`, `SALAD_PROJECT`,
`SALAD_API_KEY`, `SALAD_AI_GATEWAY_API_KEY`) would still be used instead of
what they wrote, and report it in `env_overrides`.

Environment variables follow one rule: `SALAD_` for the names the
platform, its docs and SDKs share, and `SALAD_CLI_` for everything only
this CLI reads:

| Variable | What it does |
| --- | --- |
| `SALAD_API_KEY`, `SALAD_ORGANIZATION`, `SALAD_PROJECT` | the API key, organization and project, used instead of the config file's ([Without logging in](#without-logging-in)) |
| `SALAD_AI_GATEWAY_API_KEY` | the AI Gateway key, used instead of the config file's |
| `SALAD_CLI_CONFIG_DIR` | the folder the config file is in |
| `SALAD_CLI_NO_PROMPT` | set to anything: behave as if there is no terminal, so nothing prompts, picks for you or opens a browser |
| `CI` | set to anything but empty, `false` or `0`: the same as `SALAD_CLI_NO_PROMPT` |
| `SALAD_CLI_API_BASE_URL`, `SALAD_CLI_STORAGE_BASE_URL`, `SALAD_CLI_AI_GATEWAY_BASE_URL` | test knobs, not settings: point the CLI at a stand-in server |

"At a terminal" means stdin is a terminal and neither `SALAD_CLI_NO_PROMPT`
nor `CI` says otherwise. Some agent harnesses and CI systems run commands
under a pseudo-terminal, where a prompt would wait for an answer that never
comes; GitHub Actions, GitLab and most CI systems set `CI`, and an agent can
set `SALAD_CLI_NO_PROMPT=1`. If you export `CI=true` in your own shell,
`login` will not prompt either. The installers read `SALAD_CLI_VERSION`,
`SALAD_CLI_INSTALL_DIR` and a few more (see the README).

## Container groups

Creating, updating, and waiting on container groups. Every command in this section is under
`saladcloud container-groups` (alias `cg`), so `create` below means `saladcloud container-groups create`.

Inside a group, the thing a command acts on is its one positional argument and
the group is a flag: `instances list -c my-app`, `instances restart 8a2f13ba -c
my-app` (an instance ID may be a unique prefix). `ssh` and `sftp` take the group
as their subject and the instance as `--instance`; without it they pick the
first running instance and say which on stderr. `cp` names the instance in its
remote path, `my-app/8a2f13ba:/data`. Git Bash rewrites an argument with a
slash before a colon as a Windows path list before the CLI sees it, so there
put `MSYS_NO_PATHCONV=1` in front of the command; the CLI says so when it
receives a rewritten path.

```sh
saladcloud container-groups instances list -c my-app
saladcloud container-groups instances update 8a2f13ba -c my-app --deletion-cost 1000
saladcloud container-groups ssh my-app --instance 8a2f13ba
saladcloud container-groups logs my-app --since 1h
```

Deletion cost decides which instances go first when the group scales down:
the lowest first. Raise it on an instance that is still doing work you don't
want cut off. A container can also set its own while it is busy, through the
instance metadata service; see SaladCloud's
[instance deletion cost](https://docs.salad.com/container-engine/explanation/infrastructure-platform/instance-deletion-cost)
page.

### Create flags

`create` builds its request in three layers:

1. **Base**: a copy of an existing group (`--from`), a definition file
   (`-f`), or both, with the file merged over the copy; else empty.
2. **Flags** apply on top of the base, docker-run style — a flag you pass
   always wins over the file value.
3. **Defaults** fill only the fields that are *still unset* after base +
   flags: cpu 1, memory 1024, replicas 1, restart-policy `always`,
   autostart on. Priority is deliberately not defaulted — omitted means the
   server picks.

The positional `<group>` sets/overrides the group name; with `-f` it may be
omitted when the file provides `name`, and with `--from <project>/<group>`
it may be omitted to keep the source's name. Without `-f` or `--from`,
`--image` is required.

| Flag | Type | Default | API field | Notes |
| --- | --- | --- | --- | --- |
| `<group>` (positional) | string | — | `name` | overrides the base's `name`; required unless the base provides one. Rule: 2–63 chars, lowercase letters/digits/hyphens, starts with a letter, ends with a letter or digit — validated client-side (exit 6) |
| `--image` | string | — | `container.image` | required when no `-f` base |
| `--cpu` | int | 1 | `container.resources.cpu` | vCPU cores |
| `--memory` | int | 1024 | `container.resources.memory` | MB |
| `--gpu` | string, repeatable | — | `container.resources.gpu_classes` | UUID used as-is; otherwise resolved case-insensitively against `saladcloud gpus list` names (exact match first, then unique substring; ambiguous/unknown → exit 6) |
| `--storage` | int | server default | `container.resources.storage_amount` | GB, sent in bytes |
| `--country` | string, repeatable | any | `country_codes` | two-letter code, e.g. `us`; repeat for several |
| `--replicas` | int | 1 | `replicas` | |
| `-e, --env` | `KEY=VAL`, repeatable | — | `container.environment_variables` | merges over the base env, flag wins per key; entries without `=` → exit 2 |
| `--port` | int | — | `networking` | creates `{protocol: http, port: N, auth: true}`; over a base with networking it sets only the port (and fills protocol/auth if unset) |
| `--no-auth` | bool | auth on | `networking.auth` | only valid with `--port` or a base that has networking (else exit 2) |
| `--priority` | string | server default | `container.priority` | `high\|medium\|low\|batch`; anything else → exit 6 |
| `--restart-policy` | string | `always` | `restart_policy` | `always\|on_failure\|never` |
| `--no-autostart` | bool | autostart on | `autostart_policy` | create the group stopped |
| `--display-name` | string | — | `display_name` | |
| `--queue` | string | — | `queue_connection.queue_name` | connect the group to this job queue; with `--queue-port` and `--queue-path`; the queue must exist in the project (else exit 4). Create-only (see [Job queues](#job-queues)) |
| `--queue-port` | int | — | `queue_connection.port` | the container port jobs are delivered to |
| `--queue-path` | string | — | `queue_connection.path` | the HTTP path jobs are delivered to |
| `-f, --file` | path | — | (whole request) | JSON/YAML definition; `-` reads stdin; with `--from` it merges over the copy |
| `--from` | `[project/]group` | — | (whole request) | copy an existing group in the same organization (see [Copying a group](#copying-a-group)) |
| `--allow-unknown-fields` | bool | off | — | send `-f` fields this build's API model doesn't recognize instead of rejecting them (see [Forward compatibility](#forward-compatibility)) |
| `--wait` | bool | off | — | block until the group is `running` (see [Waiting and watching](#waiting-and-watching)); conflicts with `--no-autostart` (exit 2) |
| `--timeout` | duration | `10m` | — | give up on `--wait` after this long (exit 1) |
| `-- <command>...` | words, last on the line | the image's | `container.command` | replaces the image's ENTRYPOINT and CMD; see [The container command](#the-container-command) |

`--port` alone makes the group's URL require a SaladCloud API key (the
`Salad-Api-Key` header) on every request; a public URL that anyone can call
needs `--no-auth` as well.

`autostart_policy` is a **create-time, consume-on-start intent**, not
persistent state. It tells the API whether to start the group immediately on
creation (default true; `--no-autostart` sets it false). Once the group
actually starts (`pending` → `deploying`), the API **flips the stored value
to `false`** — the intent has been consumed. So a `get` after the group has
started reports `autostart_policy: false` even though you created it with
`true`; that is expected, not a lost setting. It cannot be changed later —
see [Update patch semantics](#update-patch-semantics) — use `start` / `stop`
to control a running group.

One worked example of each style:

```sh
# 1. Flags only
saladcloud container-groups create my-app --image ghcr.io/me/app:latest --replicas 3 --port 8080 -e MODE=prod --gpu "RTX 3060 (12 GB)"

# 2. Definition file (examples/sleep-cpu.json, examples/gpu-small.yaml,
#    examples/hello-cpu.yaml are ready to use); the name given here wins
#    over the file's, because a deleted group's name is never released and
#    the file's name works only once per project
saladcloud container-groups create my-sleep -f examples/sleep-cpu.json

# 3. File as the base, flags override on top
saladcloud container-groups create sleep-two -f examples/sleep-cpu.json --replicas 2 -e DEBUG=1

# 4. A copy of an existing group, with a new image
saladcloud container-groups create my-app-v2 --from my-app --image ghcr.io/me/app:v2
```

These lines work the same in bash, zsh and PowerShell.

#### A definition on stdin

`-f -` reads the definition from stdin, so a script can keep it inline. In
bash or zsh, with a heredoc:

```sh
saladcloud container-groups create my-first-group -f - <<'EOF'
container:
  image: docker.io/library/busybox:1.36
  command: [sh, -c, 'while true; do echo hello from SaladCloud; sleep 5; done']
  resources: {cpu: 1, memory: 1024}
  priority: batch
EOF
```

In PowerShell, with a here-string piped in:

```powershell
@'
container:
  image: docker.io/library/busybox:1.36
  command: [sh, -c, 'while true; do echo hello from SaladCloud; sleep 5; done']
  resources: {cpu: 1, memory: 1024}
  priority: batch
'@ | saladcloud container-groups create my-first-group -f -
```

#### Probes

There are no probe flags; probes go in a `-f` file, as top-level fields
(`readiness_probe`, `startup_probe`, `liveness_probe`), not under
`container`. An HTTP service behind the gateway that should get traffic only
once `/health` answers 200:

```yaml
# web.yaml — saladcloud container-groups create my-web -f web.yaml --wait
name: my-web
container:
  image: ghcr.io/me/web:v1
  resources: {cpu: 2, memory: 4096}
replicas: 1
restart_policy: always
networking:
  protocol: http
  port: 8080
  auth: true
readiness_probe:
  http:
    path: /health
    port: 8080
    scheme: http
    headers: []
  initial_delay_seconds: 0
  period_seconds: 5
  timeout_seconds: 2
  success_threshold: 1
  failure_threshold: 3
```

To add or change a probe on an existing group, edit its writable document
and send it back (see [Update patch semantics](#update-patch-semantics) for
what else that document carries):

```sh
saladcloud container-groups get my-web --writable > my-web.json
# edit readiness_probe in my-web.json
saladcloud container-groups update my-web -f my-web.json
```

In Windows PowerShell 5.1, `>` writes UTF-16; write the file with
`saladcloud container-groups get my-web --writable | Set-Content -Encoding ascii my-web.json`
instead (the CLI's JSON output is ASCII).

A probe change is a new version, so every instance restarts (see
[Updating with flags](#updating-with-flags)). What each field means, and the
other probe types, are on docs.salad.com's
[health probes](https://docs.salad.com/container-engine/explanation/infrastructure-platform/health-probes)
page.

#### The container command

Words after `--` set the container command, on `create` and on `update`:

```sh
saladcloud container-groups create my-debug --image ghcr.io/me/app:latest -- sleep 2147483647
saladcloud container-groups update my-app -- python app.py --port 8080
```

The command **replaces the image's ENTRYPOINT and CMD together**, unlike
`docker run IMAGE <args>`, which keeps the entrypoint. So it starts with the
program to run: `-- python app.py --port 8080`, not `-- --port 8080` (a
command that starts with `-` exits 6). To go back to the image's own command,
send a patch file with `{"container": {"command": []}}`.

The words reach the container as your shell split them, one element each
(the platform trims spaces around each word), and run without a shell. That
differs from `cg ssh <group> -- <cmd>`, which hands its words to a shell, so
`-- "sleep 2147483647"` here is one program name, and the CLI warns. Put
saladcloud's own flags (`-o`, `--wait`, `--replicas`, …) before `--`: after it
they are part of the command, and the CLI warns about those too. For a
script, call a shell yourself and quote it on your side:

```sh
saladcloud container-groups create my-app --image python:3.12-slim -- bash -c 'echo "starting"; exec python app.py --port "$PORT"'
```

Never put a secret in the command: it is shown in full by `get`, in
`--debug` output and in warnings. Pass secrets as `-e` environment
variables, which the CLI never prints.

Single quotes keep your shell from expanding `$PORT`, so the container's
environment supplies it. By shell:

- **bash, zsh:** as above.
- **Git Bash:** put `MSYS_NO_PATHCONV=1` in front when a word starts with
  `/` or has one after `=` (`-- /start.sh`, `--prefix=/usr`); Git Bash turns
  it into a Windows path, and the CLI exits 2 when it receives one. Words
  with spaces in them (scripts) are left alone.
- **PowerShell:** `'…'` is literal, `"…"` expands `$var` locally. PowerShell
  before 7.3 can mangle `"` inside an argument; on those versions, keep the
  script free of double quotes or use a `-f` file.
- **cmd.exe:** there are no single quotes; use a `-f` file with
  `container.command` as a list.

An empty word (`""`) exits 6, because the platform drops empty command words,
which would shift the ones after it. A longer script belongs in the image
(`-- /start.sh`) or in a `-f` file.

The usual use while iterating is `-- sleep 2147483647`, which works in any
image that has a `sleep` binary (`sleep infinity` does not in busybox ones,
see [notes](notes.md#platform-behavior-to-know-about); distroless and
`scratch` images have no `sleep` at all). The container stays up without
starting your app; create it with `--wait` so an instance is running, then
`saladcloud container-groups ssh my-debug` gets you a shell to run its
entrypoint by hand (`cg ssh` needs your SSH key registered:
`saladcloud ssh-key setup`). The group runs, and is billed, until you delete
it. For a group whose container already exits at start, see
[When a group won't start or its container exits](#when-a-group-wont-start-or-its-container-exits).

#### A batch job over SSH

For a one-off job on files, keep a group up with the container command,
copy the input up, run the job over `ssh`, and copy the results back:

```sh
saladcloud container-groups create my-batch --image <image> --cpu 4 --memory 4096 --priority batch --wait -- sleep 2147483647
saladcloud container-groups cp -r ./input my-batch:/data            # makes /data/input
saladcloud container-groups cp process.sh my-batch:/data/process.sh
saladcloud container-groups ssh my-batch -- sh /data/process.sh     # the job; check its output, not the exit code
saladcloud container-groups cp -r my-batch:/data/output/. ./output  # the contents of /data/output
saladcloud container-groups delete my-batch
```

- The image needs a `sleep` binary and the tools the job uses; `cg ssh`
  and `cp` need your SSH key registered (`saladcloud ssh-key setup`).
- A one-shot `ssh` command exits 1 even when it succeeds (see
  [Exit codes](#exit-codes)), so have the script print a line you can
  check when it finishes.
- `cp -r` copies into a destination directory that exists; see
  `saladcloud container-groups cp --help`.
- In Git Bash, put `MSYS_NO_PATHCONV=1` in front of the `ssh` and `cp`
  commands, or the paths are rewritten.
- The group runs, and is billed, until `delete`.

#### Iterating on a new image

A node that has not pulled your image yet downloads it before the container
starts, and a large image can take a while. Three habits keep each try short:

- **Run the image locally before you push it, when you can.** Start it once
  with `docker run` and the environment it will get on SaladCloud, and check
  that the app starts and its probe endpoint answers: a missing dependency
  costs seconds locally and a full image download on SaladCloud. A GPU image
  may not run without a local GPU; then check what you can, such as that
  its imports load (`docker run --rm <image> python -c "import <module>"`).
- **Prove the plumbing with a small image first.** A small HTTP server is
  enough to check the queue connection, probes, the job document and
  scaling; switch to the real image once those work.
- **While testing, create with `--replicas 3`** and work on whichever
  instance is up first, since download times vary between nodes; scale back
  down when you're done. Do this before attaching a `queue_autoscaler`, which
  sets the replica count itself.

#### Copying a group

`--from` makes a new group from an existing one in the same organization:
`--from my-app` copies from the current project, `--from staging/my-app`
from another project. Copies never cross organizations.

```sh
# Try a new image next to the running group, stopped until you start it
saladcloud container-groups create my-app-v2 --from my-app --image ghcr.io/me/app:v2 --no-autostart

# Promote a group from staging into the current project under the same name
saladcloud container-groups create --from staging/my-app
```

The copy keeps everything the API returns about the source: image,
resources, GPU classes, priority, replicas, environment variables,
networking, probes, scaling, restart policy and queue connection. A `-f`
file merges over the copy and flags apply on top, so either can change any
of it. Three things differ from the source:

- **The name** is the one you give. Within the source's own project a new
  name is required (exit 2 without one).
- **The display name** becomes the new name, unless you pass
  `--display-name`.
- **Autostart** follows create's default: the copy starts unless you pass
  `--no-autostart`. The source's stored `autostart_policy` is not copied,
  because the API resets it once a group starts.

**Registry credentials are not copied.** The API never returns them, so a
copy of a private image needs them again, in a `-f` file:

```yaml
# registry.yaml
container:
  registry_authentication:
    basic:
      username: my-user
      password: my-token
```

```sh
saladcloud container-groups create my-app-v2 --from my-app -f registry.yaml
```

A `--from` copy prints a note about this on stderr unless the image is on a
public registry (Docker Hub, `ghcr.io`, `public.ecr.aws`, `quay.io`) or the
`-f` file supplies `container.registry_authentication`.

#### Scheduled scaling

The API can scale a group on a cron timetable (the portal calls this
scheduled scaling): `scheduled-scaling-enabled` plus a `scaling-actions`
list of `{replicas, schedule}` pairs, where `schedule` is a 5-field cron
expression (UTC) and `replicas` is 0–500. There are no dedicated flags —
it works through `-f` today, on both `create` and `update`, and
`get <group>` shows a `Scheduled scaling` row when a group has any of it.
`examples/scheduled-scaling.yaml` is a ready-to-run group that scales to 2
replicas at 9am and 0 at 5pm on weekdays.

Gotcha: unlike their snake_case neighbors, these two fields are
**kebab-case** in the API. A snake_case spelling (`scaling_actions`) is
rejected as an unknown field with a did-you-mean hint — as is any other
typo'd field anywhere in a definition or patch file (exit 6). To clear the
actions later, `update <group> -f` with `{"scaling-actions": []}` — the CLI
sends your patch document verbatim as a JSON merge patch, so the explicit
empty array clears the field.

**Setting replicas puts scheduled scaling on hold.** Any write to
`replicas` — `update -f` or `scale`, even to the value it already has — sets
`scheduled-scaling-enabled` to false and keeps the actions. This is a
deliberate platform rule, the thermostat model: a manual setpoint overrides
the program until you switch the program back on. Other patches (image, env,
memory, display name, the actions list themselves) leave the flag alone. An
explicit `"scheduled-scaling-enabled": true` in the same patch as `replicas`
does not survive, so re-enabling is a second write: `update <group> -f
<file>` with a file holding `{"scheduled-scaling-enabled": true}`, or
`scale <group> --replicas <n> --keep-schedule`, which does both writes for
the "new setpoint, keep the program" case. The CLI prints a `note:` on
stderr whenever a write put the schedule on hold.

### Updating with flags

`update` takes the create flags that can change on an existing group, with or
without a patch file; flags win over the file:

```sh
saladcloud container-groups update my-app --image ghcr.io/me/app:v2
saladcloud container-groups update my-app --cpu 4 --memory 8192 --gpu "RTX 4090 (24 GB)"
saladcloud container-groups update my-app -e LOG_LEVEL=debug        # other variables are kept
saladcloud container-groups update my-app -f patch.yaml --replicas 4
```

The flags are `--image`, `--cpu`, `--memory`, `--storage` (GB), `--gpu` (UUID or
name; replaces the group's GPU classes), `--country` (repeatable; replaces the
group's countries), `--replicas`, `--priority`, `--port`, `--display-name`,
`-e/--env`, and the container command after `--` (see
[The container command](#the-container-command)). The API replaces the whole set of
environment variables on every write, so `-e` reads the group's current
variables and sends them back merged with yours; in a patch file without
`-e`, `environment_variables` replaces the set as written. The name, restart
policy, autostart, gateway auth, and queue connection cannot change after
create, so there is no flag for them. Either way the change goes through the same settle and
verification as a patch file.

A change to the image, the command, environment variables, probes or
gateway settings makes a new version of the group: every instance restarts
onto it as its node next checks in, and old and new never run side by side.
Each instance downloads the whole new image again, even on a machine that
ran the previous version, so with one replica the gap lasts the full
download plus your app's start-up.
A resource change (`--cpu`, `--memory`, `--gpu`, `--storage`) moves only the
instances whose machine no longer fits; replicas, the display name and
autoscaling restart nothing. To avoid downtime, run the new version as a
second group (`create my-app-v2 --from my-app --image …`), check it, then
switch your clients over; see SaladCloud's
[deployment strategies](https://docs.salad.com/container-engine/how-to-guides/deployment-strategies).

### Update patch semantics

`update <group> -f <file>` validates your document (strict: unknown fields
anywhere are rejected with the offending path and, for a close misspelling
of a known field, a did-you-mean hint; exit 6 — unless
`--allow-unknown-fields`, see [Forward compatibility](#forward-compatibility))
and then sends it
**verbatim** as an [RFC 7386 JSON merge patch](https://www.rfc-editor.org/rfc/rfc7386):
fields you name are replaced, `null` clears a field, arrays replace
wholesale, and fields you omit are untouched. An empty patch (`{}`, empty
file, or bare `null`) is rejected client-side.

To edit an existing group, start from `get <group> --writable`: it emits the
group as JSON with the response-only and create-only fields removed and
`priority` moved under `container`, so `get --writable > g.json`, edit,
`update -f g.json` round-trips without the unknown-field rejection a raw
`get -o json` document trips (it also composes with `jq`). The document
includes `replicas`, and writing `replicas` puts scheduled scaling on hold
(see [Scheduled scaling](#scheduled-scaling)); the CLI prints a note when
that applies — delete the key unless you mean to change it. Verified caveats where the
API deviates from clean merge-patch behavior:

- **`environment_variables` is replace-not-merge server-side**: the map you
  send becomes the whole map — unmentioned keys are lost, and a `null` value
  is stored literally rather than deleting its key. Send the complete
  desired map.
- **`container.priority` is patchable on GPU groups, ignored on CPU-only
  ones**: priority is a GPU scheduling tier (see the per-tier prices in
  `saladcloud gpus list`), so on a group with `gpu_classes` a patch works
  normally, except that a running or deploying group can lower its priority
  only after 60 minutes at the current one (exit 6; see
  [notes](notes.md#platform-behavior-to-know-about)). On a CPU-only group the API validates the value but discards it —
  the patch returns 200 and the priority does not change. Note that *create*
  does store a non-batch priority on a CPU-only group, so the two paths
  disagree; treat priority on CPU-only groups as meaningless either way.
  Patching top-level `priority` is always wrong — the field lives under
  `container` in requests even though responses return it top-level (the CLI
  rejects the top-level spelling as an unknown field).
- **Updates settle asynchronously**: after `update`, `pending_change` is
  true and `get` keeps returning the *previous* config (with
  `current_state.description: "Version N is pending."`) until the change
  lands — so "patch then `get` to assert" can legitimately disagree with
  your patch for a while. A second `update` during that window is rejected
  (`pending_update_in_progress`). `update` waits for the change to settle
  by default (up to `--timeout`, default `2m`) and emits the settled
  document, so back-to-back updates work; `--no-wait` returns the
  pre-settle response instead and prints a note that the change was not
  verified.
- **`autostart_policy` is create-time only and cannot be patched**: it is
  not part of the patch schema, so an `update` document containing it is
  rejected as an unknown field (exit 6). It is a one-shot "start on create"
  intent — see the note under [Create flags](#create-flags) — not persistent
  state you can toggle later. To start or stop an existing group, use
  `saladcloud container-groups start` / `stop`.
- **Names must be unique within the organization and project, including
  against deleted groups.** Deletion is a soft delete and never releases the
  name, so re-creating a name that was ever used returns `name_conflict`,
  however long ago `list`/`get` stopped showing the group. Pick a different
  name.
- **Setting replicas holds scheduled scaling** — see
  [Scheduled scaling](#scheduled-scaling); the CLI prints a note when a
  write did this.

### Forward compatibility

<!-- cspell:ignore enviroment -->

The CLI's request/response model is a pinned snapshot of the API (the
generated SDK), so the API can grow past any given build. The posture:

- **Responses are read leniently**: fields this build doesn't know are
  ignored, new status values pass through as-is. New API *output* never
  breaks an old CLI.
- **Requests are checked strictly by default**: an unrecognized field in a
  `-f` definition or patch is rejected (exit 6, kind `unknown_field`) naming
  the path — because it is usually a typo, and a typo'd field sent anyway is
  a silent misdeploy. A close misspelling of a known field (`replica`,
  `enviroment_variables`, a kebab/snake mix-up) gets a did-you-mean hint,
  and the escape hatch below is not offered for it. A real field that this
  build's schema does not accept on that verb (create-only, response-only,
  or spelled differently in requests) gets an explanation instead — stated
  as a fact about this build's model, since the API may have moved.
- **`--allow-unknown-fields`** (on `create` and `update`) is the explicit
  acknowledgement for the remaining case: the field is real and newer than
  this build, so nothing here recognizes it. Each unrecognized field is
  warned on stderr and sent to the API as written. One caveat on `create`: fields nested inside **array
  elements** (e.g. a new per-`scaling-actions` key) cannot be preserved —
  create rebuilds arrays from its typed model, so those are warned as
  DROPPED; set them with `update -f`, which sends your document verbatim.

When a rejection is a genuine API-model gap rather than a typo, the fix is
to rebuild against a regenerated SDK; the flag is the bridge until then.
The same pinned model drives `get --writable`: a field newer than this
build is filtered out of the writable document (and listed in the stderr
note), the mirror image of strict mode rejecting it in a patch.

### Waiting and watching

Two different needs, two different mechanisms:

- **`update` settles by default.** A patch that stages (an image change,
  say) returns `pending_change: true` and the response is still the
  previous config, so `update` polls until the change lands, then emits
  the settled document and runs its write-verification checks. `--timeout`
  (default `2m`) caps the wait and exits 1
  with kind `wait_timeout` whose message says the patch itself was accepted
  and only verification timed out. `--no-wait` skips the wait and the
  checks, returning the immediate response with a stderr note when the
  change is pending. Be clear about what that gives up: under `--no-wait`
  nothing compares what you asked for with what the API did, so it is the
  one write path where a silently discarded or altered field goes
  unreported.
- **`--wait` / `--timeout`** (on `create`, `start`, `stop`) is for
  scripts, CI, and agents: block until the group reaches the verb's goal
  (`running` for create/start, `stopped` for stop), then exit 0. Polls every 5s; `--timeout` (default `10m`) caps
  the wait and exits 1 with kind `wait_timeout` — the group keeps going
  server-side, so check `saladcloud container-groups get <group>` or retry with a longer
  `--timeout`. Ctrl-C exits 1 with kind `interrupted` and empty stdout (the
  server continues regardless); a second Ctrl-C kills the CLI at once.
  A wait for `running` does not sit out the timeout on a group that cannot
  get there: once the group has been seen `pending` or `deploying` (for
  `create`, from the start), reaching `stopped`, `failed` or `succeeded`
  exits 1 at once with kind `group_stopped`, `group_failed` or
  `group_succeeded`. A `stopped` read before that is the API not having
  acted on `start` yet and is waited through, and so is a group still
  `pending` or `deploying`, until `--timeout`; `pending` can last a long
  time (see [When a group won't start or its container exits](#when-a-group-wont-start-or-its-container-exits)).
  `running` means the instances run, not that they serve: when the group
  has a readiness probe and a running instance is not passing it yet, a
  wait for `running` still succeeds but prints a stderr note
  (`note: 0 of 1 running instances ready …`); `instances list` shows each
  instance's READY.
  Transient API errors (network, 5xx, 429) never abort a wait — the CLI
  warns on stderr and keeps polling. Progress lines go to stderr (table mode
  only) whenever the status or instance counts change, and once a minute
  while they do not, so a long `pending` never looks hung. A group stays
  `deploying` while its instances download the image, which can take a
  while for a large one; `saladcloud container-groups instances list -c
  <group>` shows each instance's state and pull progress. **A waiting command
  emits its stdout payload only after the wait succeeds**: the single JSON
  document (or table) describes the settled state, and on timeout or
  interrupt stdout is empty — scripts can trust that output means success.
  `create --wait` conflicts with `--no-autostart` (exit 2): a group created
  stopped never becomes running.
- **`-w, --watch` / `--interval`** (on `list`, `get`, `instances list`,
  `orgs list`) is for humans: clear the screen and re-render the table every
  `--interval` (default 5s, minimum 2s) until Ctrl-C (exit 0), like
  `watch(1)`. It is table mode only: `-w -o json` is a usage error, because
  scripts want `--wait` or a loop over `get -o json`. When stdout is not a
  terminal, renders are separated by a blank line and no control codes are
  written.

The composable pattern for a deploy pipeline:

```sh
saladcloud container-groups create my-app --image ghcr.io/me/app:latest --wait --timeout 15m \
  && saladcloud container-groups logs my-app -f
```

## Logs

`saladcloud logs` searches application logs and system events (allocation,
start, probe and stop events) at the scope you give:

```sh
saladcloud logs                           # the whole organization, every project
saladcloud logs --project prod            # every container group in one project
saladcloud container-groups logs my-app   # one container group, in the current project
```

`logs` is organization-scoped and takes its filters as flags; `-c <group>`
narrows it to one container group, the same query as `container-groups logs
<group>`.

Only an explicit `--project` narrows a query without a container group: the
saved default project and `SALAD_PROJECT` do not, so `saladcloud logs` always
means the organization; in table mode a stderr line names the scope
(`scope: organization my-org, all projects`). Following a failed or stopped
group says on stderr that nothing will be logged. When the scope is wider than one container group,
each table line names its source as `[project/group/instance]` (or
`[group/instance]` for a project). With `-o json` every entry is printed
whole, including its `resource.labels`.

The other filters work at every scope: `--since`/`--until` (default the last
15 minutes), `--severity`, `--filter` for a substring, `--query` for a raw
[log query](https://docs.salad.com/container-engine/reference/log-queries),
and `--instance` (a full instance ID, or a unique prefix when a container
group is given). `-f` follows new entries until Ctrl-C.

Entries come from three sources, in `resource.type`: `container` is your
app's stdout and stderr; `instance_controller` and `deployment_controller`
are system events. `--query` picks one:

```sh
saladcloud container-groups logs my-app --query 'resource.type != "container"'   # system events only
saladcloud container-groups logs my-app --query 'resource.type = "container"'    # your app's output only
```

### When a group won't start or its container exits

A new group starts in `pending` while SaladCloud pulls its container image.
For a large image, or when the platform is busy, that can take a long time,
and nothing is logged for the group yet. Once the image is pulled the group
moves to `deploying` and SaladCloud looks for machines to run it on; from
then on the system events show each step. Be patient while a group is
`pending`; if it stays there for a very long time, contact SaladCloud
support.

1. **Check the system events first:**
   `saladcloud container-groups logs <group> --since 1h --query 'resource.type != "container"'`.
   They show each instance being allocated to a machine, downloading the
   image, starting, passing its probes, and exiting (`Exited:3 (Error)` is
   the container's exit code).
2. **Then your app's output:** the same command with
   `--query 'resource.type = "container"'`. Only what the app writes to
   stdout and stderr is collected, so an app that logs to a file shows
   nothing here.
3. **To look inside a container that exits at once,** keep it up without
   its entrypoint and run the entrypoint by hand:

   ```sh
   saladcloud container-groups update <group> -- sleep 2147483647
   saladcloud container-groups ssh <group>       # then run the image's start command and watch it fail
   ```

   `cg ssh` needs your SSH key registered (`saladcloud ssh-key setup`), and
   the image needs a `sleep` binary (see
   [The container command](#the-container-command)). When you're done, put
   the image's own command back with
   `saladcloud container-groups update <group> -f patch.json`, where
   `patch.json` is `{"container":{"command":[]}}`.

## Availability

`saladcloud availability` asks how many nodes could take one replica with
the given requirements right now, per priority tier. It is the only command
that asks: availability queries are expensive on the platform, so `gpus`
lists the catalog and prices only, and nothing calls availability behind
your back. Call it once before `create`, not in a loop.

```sh
saladcloud availability --gpu "RTX 4090 (24 GB)" --gpu "RTX 3090 (24 GB)" --cpu 8 --memory 16384
saladcloud availability --from my-app --gpu "RTX 5090 (32 GB)"   # my-app's requirements, another class
saladcloud availability --cpu 4 --memory 8192                    # CPU-only nodes
```

The flags are the requirements of one replica, named and resolved as on
`create`: `--gpu` (UUID or name, repeated for several classes — names
contain commas, so values are never comma-split), `--cpu`, `--memory` (MB),
`--storage` (GB) and `--country` (repeatable). `--from <group>` or
`<project>/<group>` starts from an existing group's GPU classes, cpu,
memory, storage and countries; explicit flags override them. Without
`--gpu` the query is for CPU-only nodes.

Each class named is one query and one row; the JSON is
`{"items": [...], "combined": ...}`. With two or more classes (on the
command line or from `--from`), one more query asks for all of them at once
and is shown as "any of the above (one platform query)", JSON `combined`
(null with one class): the nodes a group listing those classes can draw
on. It is its own query, taken separately from the per-class ones, so it
neither equals nor bounds their sum (a node carrying two of the classes
counts once in it, and the counts move between queries), and the CLI never
adds rows up. A query whose counts all come back zero is asked once more
before it is shown, so the worst case is twice the queries. Counts are
estimates of currently online nodes, one per priority tier. The on-call
count (`on_call_gpu`, `on_call_cpu` in JSON) is an `ON-CALL` column only
when a row reports one other than 0, which the API does not do today. A zero is not proof of
no capacity: an overloaded availability service answers 200 with every
count 0, so a row that is all zeros after the retry gets a stderr warning
that the availability API may be overloaded (one class can be all zeros while others answer). A CPU-only answer has `combined` too,
always null, so every availability document has the same keys.

## Inference endpoints & transcription

`saladcloud inference` is both the product area and its resource: an
inference endpoint is what `inference list` lists, so `inference` is the one
singular noun that takes `list` and `get`. Inference endpoints are
**organization-scoped** — no project is involved. `inference list` fetches
every page (the API pages at 25) and emits `{"items": [...]}` like every
list. `inference jobs list` lists newest first, and `--limit` keeps the
newest N: 100 by default, `0` for all up to 10,000, the most one listing
returns. A stderr note says when a listing stopped there, and in table mode
when the limit left older jobs out. The API lists jobs oldest first and
gives no total, so the CLI finds the last page with a short search and
reads backwards from it, a few dozen requests at most.

Two layers, split by one rule:

> **The plumbing reaches every endpoint**: `inference jobs list|get|create|cancel
> ... --endpoint <endpoint>` queues and reads jobs on any endpoint, including
> one newer than this CLI. **Each endpoint the CLI knows also gets a porcelain
> command named exactly after it** — `inference transcribe`, `inference
> transcription-lite` — whose flags are that endpoint's input fields,
> kebab-cased, so two endpoints never spell one input differently. Porcelain is
> pure client-side composition over the plumbing (upload, queue, wait, print),
> never its own API surface. A porcelain command never moves to the top level,
> and the names `list`, `get`, `jobs`, `help` and `completion` are reserved: an
> endpoint with one of those names is reached through `inference jobs create
> --endpoint <name>`.

```sh
# What managed endpoints does my org have, and what do they cost?
saladcloud inference list
saladcloud inference get transcribe              # price row, description, input fields

# Transcribe a local recording: uploads to S4, waits, prints the transcript
saladcloud inference transcribe ./meeting.mp3

# Subtitles for a video already on the web, on the cheaper lite tier
saladcloud inference transcription-lite https://example.com/talk.mp4 --srt

# Fire and forget, then poll with the plumbing
saladcloud inference transcribe ./meeting.mp3 --no-wait
saladcloud inference jobs get <job-id> --endpoint transcribe

# The plumbing equivalent (any endpoint, any input)
saladcloud inference jobs create --endpoint transcribe \
  --input '{"url": "https://example.com/a.mp3", "language_code": "en"}' --wait
```

Transcription details:

- The argument is an http(s) URL (used as-is) or a local file path. Local
  files upload to the organization's S4 storage (Salad Simple Storage
  Service, `storage-api.salad.com`) under `saladcloud-cli/transcribe/`, with
  a **signed URL that expires after 24 hours**; the upload is streamed, and
  a table-mode note on stderr confirms it. The upload is deleted once the
  transcript is back. It stays when the job fails (a stderr note names it,
  for inspection), with `--no-wait` (the queued job still reads it), and
  with `--keep-upload`. In `-o json` the signed URL's token is removed from
  the job's `input.url` unless `--keep-upload`.
- Each input field of the endpoint is a flag: `transcribe` takes 19 besides
  the URL (`--language-code`, `--diarization`, `--sentence-diarization`,
  `--word-level-timestamps`, `--srt`, `--translate`, `--summarize`,
  `--llm-translation`, `--custom-vocabulary`, `--enhanced-accuracy`, and the
  rest in `--help`), `transcription-lite` takes 8, and a flag the endpoint
  does not take is rejected. **A flag you do not set is not sent**, so the
  endpoint's own defaults apply (every boolean off; on `transcribe` an unset
  `language_code` means the language is detected automatically, while forcing
  one makes the audio be treated as that language);
  the request is the one `inference jobs create --input` would send for the
  same intent. `--input-extra '<json>'` merges raw fields **over** the built
  input (extra wins), for fields newer than this CLI.
- Waiting is the default (porcelain's whole point): poll every 5s,
  `--timeout` default `30m` for long audio. On success, table mode prints
  the transcript text — the SRT document with `--srt`, the result URL with
  `--return-as-file`; `-o json` emits the full final job document. A failed
  job exits 1 (kind `transcription_failed`; a canceled one, `job_canceled`)
  with the status and last event. `--no-wait` prints the job id
  and the `inference jobs get` command to poll it.
- Failure detection: the transcription API marks some failed jobs
  `succeeded` (for example an undownloadable URL) with the real failure in
  `output.error`/`error_code`; the CLI treats those as failures (exit 1,
  kind `transcription_failed`, stdout empty) rather than printing the error
  blob as a transcript. Similarly, `--srt` errors out if the job returned no
  SRT document (`output.srt_content`), and `--return-as-file` if it returned
  no `output.url`, instead of silently printing something else.
- Pricing: transcription is billed per audio hour — see SaladCloud pricing,
  or the endpoint's price row via `inference get <endpoint>`.

`inference jobs create` takes exactly one input source: `-f <file>` is the
full job prototype document (`{input, metadata?, webhook?}`, JSON or YAML),
while `--input '<json>'` is the input object alone, wrapped into the
prototype for you. `--wait` polls to a terminal status: `succeeded` emits
the final job and exits 0; `failed`/`cancelled` exit 1.

## Job queues

A job queue holds jobs for a container group's instances to work through.
Queues belong to a project. A group is connected to one when it is created —
`--queue <name> --queue-port <n> --queue-path <path>` on `container-groups
create`, and the queue must already exist — and its instances then receive
the queue's jobs over HTTP on that port and path. The connection is fixed
for the group's life: `update` cannot add, change or remove it, so moving a
group to another queue means a new group (`create --from`).

The container image must run the
[job queue worker](https://docs.salad.com/container-engine/how-to-guides/job-processing/queue-worker)
beside your HTTP server: the worker takes jobs from the queue and forwards
them to that port and path. Without it the group starts and runs, its jobs
stay `pending`, and nothing reports why, so `create` says so on stderr
whenever it connects a queue. Give the group a readiness probe on your
server's port (in a `-f` file; `readiness_probe` is a top-level field of
the definition, not a key under `container`): the worker waits for the probe
to pass before taking a job. Without one it can take jobs before your server
listens, and each such job uses up its four attempts within seconds and
fails. While you build the worker image,
[Iterating on a new image](#iterating-on-a-new-image) and
[The container command](#the-container-command) show how to test with a
small image and debug startup over ssh.

```sh
saladcloud queues create my-queue --display-name "My queue"
saladcloud container-groups create my-worker --image ghcr.io/me/worker:latest \
  -f probe.yaml --queue my-queue --queue-port 8080 --queue-path /job
saladcloud queues jobs create --queue my-queue --input '{"prompt": "hello"}' --wait
# the same job from a file: -f takes the whole job document, so the prompt sits under "input"
#   {"input": {"prompt": "hello"}}
saladcloud queues jobs list --queue my-queue
```

In Git Bash, put `MSYS_NO_PATHCONV=1` in front of a `create` with
`--queue-path`: Git Bash turns the leading `/` into a Windows path
(`C:/Program Files/Git/job`), which the API would store and post every job
to, so `create` exits 2 when it receives one.

`probe.yaml` here is

```yaml
readiness_probe:
  http: {path: /health, port: 8080, scheme: http, headers: []}
  initial_delay_seconds: 0
  period_seconds: 5
  timeout_seconds: 2
  success_threshold: 1
  failure_threshold: 3
```

To scale the group with the queue's length, set `queue_autoscaler` with
`update` once the group exists; the API drops it on create
([notes](notes.md#platform-behavior-to-know-about)), and `create` warns when
it does. `min_replicas`,
`max_replicas` and `desired_queue_length` are the fields that matter. The
group's `replicas` then follow the autoscaler (with `min_replicas` 0 they
can go to 0), and `update` warns when that happens:

```sh
# bash, zsh
saladcloud container-groups update my-worker -f - <<< '{"queue_autoscaler": {"min_replicas": 0, "max_replicas": 3, "desired_queue_length": 1}}'
```

```powershell
# PowerShell
'{"queue_autoscaler": {"min_replicas": 0, "max_replicas": 3, "desired_queue_length": 1}}' | saladcloud container-groups update my-worker -f -
```

A queue job is the same `{input, metadata?, webhook?}` document as an
inference job, and `queues jobs create` takes the same `-f` / `--input`
sources, plus `--webhook <url>` for the URL notified when the job finishes
(signed with the key `saladcloud webhook-secret get` prints). There is no
porcelain: a queue job's output is whatever your container returns. A job
that fails is retried at once, up to four attempts in all.

`queues jobs cancel` stops a pending job from running. A running job is
marked `cancelled` and its output discarded, but the instance working on it
runs it to the end, so make handlers with side effects idempotent; a
finished job cannot be canceled (exit 1). `queues delete` deletes the
queue's pending jobs too, and every group connected to it loses the
connection for good. Stopping or deleting a group does not stop its worker
at once: it can take new jobs for some seconds, and those read `running`
for several minutes before they are retried
([notes](notes.md#platform-behavior-to-know-about)). Stop submitting, or let
the queue drain, before you stop a group.

`queues jobs list` lists newest first and takes `--limit` (100 by default).
The API lists queue jobs in job ID order, not by time, so the CLI reads
pages and sorts them by creation time: with a limit it reads at most 1,000
jobs (or the limit, if larger) and says on stderr when the queue may hold
more, since the newest are then the newest of those read; `--limit 0` reads
every job, up to 10,000. The API serves 25 jobs a page, so a full read of a
long queue takes 40 requests per 1,000 jobs, and close to a minute. A job ID
must be the full UUID on both job nouns (`get`, `cancel`); unlike an
instance ID, a prefix is not resolved.

## AI Gateway

The [SaladCloud AI Gateway](https://docs.salad.com/ai-gateway/explanation/overview)
is an OpenAI-compatible endpoint at `https://ai.salad.cloud/v1` (chat
completions and `/v1/models`, in beta). It uses **its own organization-scoped
key**, not the SaladCloud API key. The portal's
<https://portal.salad.com/ai-gateway> page lists your organizations and
links to each one's key page; store the key with:

```sh
saladcloud ai-gateway login                        # at a terminal: asks for the key (hidden; Enter opens the page above), checks and saves it
saladcloud ai-gateway models list                  # the model ids the gateway serves (public: no key needed)
saladcloud ai-gateway test --prompt "say hello"    # one completion: proves the key is live and billed (thinking off; --thinking to see the model reason)
saladcloud doctor                                  # ai-gateway-api-key-present row: pass when a key is set (not tested)
```

Without a terminal, pass the key with `--api-key -`, which reads the first
line of stdin (bash or zsh: `printf '%s' "$KEY" | saladcloud ai-gateway login
--api-key -`; PowerShell: `$env:KEY | saladcloud ai-gateway login --api-key
-`), or set `SALAD_AI_GATEWAY_API_KEY`: with only the variable, `ai-gateway
login` checks the key and saves nothing. The check sends one billed request
(a fraction of a cent, to the key's organization). A key whose organization
is out of credits is saved anyway, with a warning, and `-o json` reports
`"credits_available": false`; a key without AI Gateway access (403) is
refused, exit 3. `ai-gateway logout` removes the saved key and warns when
`SALAD_AI_GATEWAY_API_KEY` still supplies one.

Two gateway quirks the CLI works around: `/v1/models` is public and answers
the same to any key or none (so `ai-gateway models list` needs no key), and
`ai-gateway login` therefore validates with a one-token
completion instead (`doctor` only reports that a key is set, because a check
costs a completion; `ai-gateway login` and `ai-gateway test` are the two commands that spend one); and the hosted models are reasoning models
that spend hundreds of tokens thinking before the first word of an answer,
so `ai-gateway test` turns thinking off (`chat_template_kwargs.enable_thinking:
false`, the one switch the gateway honors) and answers in a few tokens;
`--thinking` opts back in with a 1024-token budget.

Keys are created in the portal, which is why `ai-gateway login` asks for
the key rather than fetching it itself. The model
`ai-gateway test` uses by default is the first model id in sorted order. To actually use the
gateway, point any OpenAI client or `curl` at the endpoint with the key as a
bearer token; the CLI deliberately has no chat verb, because every OpenAI
client library already speaks the gateway's API. Pricing and the
subscription tier: <https://docs.salad.com/ai-gateway/reference/pricing>.

## Checking your setup

`saladcloud doctor` runs ordered checks and prints one row each, `CHECK
STATUS DETAIL`:

| Check | What it checks |
| --- | --- |
| `config` | the config file can be read and saved |
| `api-reachable` | the API answers (any response will do) |
| `api-key` | the API accepts the key |
| `organization` | the organization exists for the key |
| `project` | the project exists in it, or that `default` would be used |
| `ai-gateway-api-key-present` | an AI Gateway key is set; it is not tested, because a test costs a billed request (`saladcloud ai-gateway test` sends one) |
| `local-ssh-key-present` | a local SSH key exists; whether it is registered cannot be checked (register it at <https://portal.salad.com/ssh-keys>) |

Each row is `pass`, `warn`, `fail` or `skip`, and its detail says what was
found, with where a value came from (`(from config)`, `(from
SALAD_ORGANIZATION)`). A row that is not `pass` is followed on stderr by
`hint: <check>: <fix>`; in JSON every row carries `hint`, `null` when there
is none. A check that needs an earlier one is `skip` and says what it could
not check. A variable that overrides a saved value is named after the table,
and in JSON as `env_overrides` beside `items`.
`doctor` exits 0 when no row failed, so a warning never fails a script, and
someone who uses only the AI Gateway passes; otherwise it exits 1 naming
the failed checks (`1 check failed: api-key`).

## Debugging requests

`--debug` (on any command) prints one line per API request on stderr:
method, URL, status and duration. `--debug=body` adds the request headers
and both bodies. Credentials never reach that output: the `Salad-Api-Key`
and `Authorization` headers are redacted; in JSON bodies, so are credential
fields (`secret_key`, `api_key`, registry and log-sink passwords and
tokens), the value of every header in a headers list (a log sink's or a
probe's), and every environment variable's value, with the names left in
place; signed S4 URL query strings are redacted, and a file upload's body
is not shown. JSON bodies are printed compact, keys sorted.

```sh
saladcloud container-groups get my-app --debug
saladcloud container-groups update my-app --replicas 2 --debug=body 2> debug.log
```

## Coding agents

`saladcloud changelog` prints the changelog embedded in the binary, so an agent that learned this CLI from older
material can catch up without network access: `saladcloud changelog --since 2026-09-01` or `--since 1.0.0` prints only
the releases newer than that (the Unreleased section is always included). In JSON mode the result is
`{"version","since","items":[{"version","date","body","title"}]}`, one item per release; an unparseable `--since` is a usage error (exit 2).

`saladcloud agent init` writes a short SaladCloud section into the file
coding agents read for project instructions, so an agent working in the
repository knows to use this CLI, how it authenticates, that setup is yours
(it asks you to run `saladcloud login` in your own terminal and never asks
for a key in the chat), that an error's `hint` is its fix, its exit codes,
and where the SaladCloud skills are:

```sh
saladcloud agent init                 # AGENTS.md, or CLAUDE.md when run under Claude Code
saladcloud agent init --file docs/AGENTS.md
saladcloud agent init --print         # preview the block; writes nothing
saladcloud agent init -o json         # {"file":"AGENTS.md","action":"created|updated|unchanged","ok":true,"reason":...,"version":...}
                                      # (with --print: "action":"printed" and "file":null)
```

The target is `--file` if given; otherwise `CLAUDE.md` when `CLAUDECODE` is
set in the environment (Claude Code sets it for every process it spawns and
reads `CLAUDE.md`, not `AGENTS.md`) or when the directory already has a
`CLAUDE.md` and no `AGENTS.md`; otherwise `AGENTS.md`. The block sits between
`<!-- SALADCLOUD START -->` and `<!-- SALADCLOUD END -->` markers: re-running
replaces it in place and leaves the rest of the file alone, a run that changes
nothing exits 0 with `"action": "unchanged"`, and a file with a broken pair of
markers is left untouched and reported as invalid input (exit 6). Line endings
follow the existing file. The command never calls the API and needs no
credentials.

The block deliberately carries only facts that are stable across releases —
the platform's shape and this CLI's contracts — and tells the agent to install
the SaladCloud skills (`npx skills add https://docs.salad.com`) for
everything else, so there is one source of guidance that evolves and one
that does not.

## Exit codes and JSON output

The contracts scripts and agents build on. During the beta none of them is promised: any may change, and every
change is listed in the [changelog](../CHANGELOG.md).

### Exit codes

Part of the CLI's public contract (`internal/clierr`) — scripts and agents
depend on them, so a change to a code is listed in the changelog.

| Code | Meaning |
| --- | --- |
| 0 | ok |
| 1 | generic error (API 5xx, Ctrl-C, unexpected failures) |
| 2 | usage error: the command line cannot be parsed or is structurally wrong |
| 3 | authentication failed (401/403, or no key at all) |
| 4 | not found (404) |
| 5 | rate limited (429, after retries) |
| 6 | invalid input: a value breaks a platform rule (400/422, or caught before the request), or a file you named cannot be read or parsed |
| 7 | network error (no HTTP response) |
| 10 | not available: the AI Gateway declined the request for billing reasons (402) |
| other | `container-groups ssh`, `sftp` or `cp`: the ssh/sftp child's own exit code, kind `child_exit` |

The CLI checks what it can before any request, and the code says who
decides: 2 when the CLI alone can tell the command line is wrong (an
unknown flag, a missing argument, `-e KEY` without `=`, an unparseable
duration, a malformed `group/instance:path`, `--interval` below the
minimum, `--status` outside the list it filters on); 6 when the line parses
but a value breaks a platform rule (a range, an enum such as `--priority`
or `--restart-policy`, a GPU class name that does not resolve, the
group-name pattern, an unknown `-f` field, or a container command the
platform could not run); 3 when there
is no credential at all, or one no key can be. An organization or project
name from `--org`, `--project`, `SALAD_ORGANIZATION` or `SALAD_PROJECT` that
cannot be a URL name exits 6 before any request, with a guess in the hint,
on the commands that use it (`orgs` does not, so it lists your organizations
whatever the variable holds; `whoami` reports the name instead of failing).
A value there that looks like a key (longer than any name, or equal to an
API key or AI Gateway key in use or saved) is never shown: the error says
`SALAD_ORGANIZATION looks like an API key, not an organization name` and
exits 6, `config set organization` refuses one, and every command that
shows a saved value (`config list`, `logout`, `doctor`, `whoami`) shows it
as hidden; only `config get` returns it. Names are trimmed of
surrounding spaces and line endings, so a variable read from a
Windows-saved file works.
One exit 2 comes after a request: when no project was set and the
organization has no `default` project, a project-scoped command exits 2
(kind `usage`, "no valid project set") once its 404 has been confirmed to
be the project's, because the fix is on the command line or in `use`.

Errors about a value name it and where it came from, and the hint fixes it
there: `API key ****...ABCD (from SALAD_API_KEY) was rejected` says to
change or remove the variable (or to use the saved key it hides), `(from
config)` says to run `login` again, and the same goes for the AI Gateway
key. When a command gets a 404 under an organization, the CLI checks the
organization once: if your key can no longer see it, the error is
`organization "old-org" (from config) not found`, with your organizations
listed at a terminal (`saladcloud orgs list` lists them otherwise).

A usage error says what to type instead. A parent given where its flag
belongs gets the corrected command in its hint (`instances list takes the
group as -c, not as an argument`, then `hint: run saladcloud container-groups
instances list -c my-app`); an unknown flag's nearest spelling is in the hint
too;
a missing argument or required flag names it and prints the usage line; a
flag value of the wrong type says what it should be (`invalid --replicas
"two": not a number`); `--no-wait` on a verb that waits only on request, or
`--wait` on one that waits by default, says which default applies. An
unknown command lists up to four commands of that name elsewhere in the
tree, a noun followed by the verb you typed, or the nearest spelling of a
typo.

`container-groups ssh`, `cp`, and `sftp` pass the child ssh/sftp process's
exit code through as the CLI's own, as `kubectl exec` and `docker exec` do:
when the child fails, the error document carries kind `child_exit` and the
child's code. The CLI's own failures before the child starts (no running
instance, no ssh on PATH) keep their own kinds and codes. Caveat: the
SaladCloud relay does not propagate one-shot command exit codes, so
`container-groups ssh <group> -- <cmd>`
will exit 1 even when the command succeeded (the CLI prints a stderr note);
`cp` (SFTP) exit codes are reliable. A one-shot command's output also comes
through a terminal, so its stderr is mixed into stdout and programs may add
color; move files with `cp`, not `ssh -- cat`.

### Error kinds

Every error document's `kind` is one of these; the set is closed, so a
script can switch on it. A new failure mode maps onto an existing kind.

| Kind | Exit code | Meaning |
| --- | --- | --- |
| `usage` | 2 | the command line is wrong: unknown flag or command, missing argument, unparseable value |
| `invalid_input` | 6 | a value breaks a platform rule, or a file you named cannot be read or parsed; also the API's 400/422 |
| `unknown_field` | 6 | a definition, patch or job file has a field the API does not take (see [Forward compatibility](#forward-compatibility)) |
| `auth` | 3 | no API key, or the API rejected it (401/403) |
| `not_found` | 4 | the resource does not exist (404) |
| `rate_limited` | 5 | the API kept answering 429 after the CLI's retries |
| `network` | 7 | no HTTP response: DNS, connection, timeout, TLS |
| `not_available` | 10 | the AI Gateway declined the request for billing reasons (402) |
| `api_error` | 1 | any other API failure (other 4xx, 5xx, an undecodable response), local I/O on files the CLI chose, or a `doctor` check failed |
| `internal` | 1 | the CLI could not encode or copy its own data; a CLI bug, worth reporting |
| `interrupted` | 1 | Ctrl-C during a wait (`--wait`, an `update`/`scale` settle, `inference transcribe`), a prompt or a request; end of input at a prompt; "n" at `ssh-key setup`'s generate prompt (`<command> canceled`, for example `container-groups list canceled`); what the server already received goes ahead. `logs --follow` and `--watch` end with exit 0 instead |
| `wait_timeout` | 1 | a wait ran past `--timeout`; the server keeps going |
| `group_stopped` | 1 | a wait for running saw the group stop instead |
| `group_failed` | 1 | a wait for running saw the group fail |
| `group_succeeded` | 1 | a wait for running saw the group's containers exit successfully |
| `job_failed` | 1 | `inference jobs create --wait` or `queues jobs create --wait` saw the job fail |
| `job_canceled` | 1 | `inference jobs create --wait`, `queues jobs create --wait`, `inference transcribe` or `inference transcription-lite` saw the job canceled |
| `transcription_failed` | 1 | a transcription job failed, or finished with an error in its output |
| `child_exit` | the child's | `ssh`, `cp` or `sftp` exited nonzero; the CLI exits with its code |

### JSON contract

- With `-o json`, every command emits exactly one JSON document on stdout.
  The exceptions are `logs`, which emits NDJSON (one complete JSON object
  per log entry, flushed line by line, also with `--follow`), and
  `container-groups ssh` and `sftp`, whose stdout is the remote session's
  (`ssh --config` prints ssh_config text), and `completion` and `help`, which
  print a shell script and help text. `cp` emits one result document; in
  table mode sftp's output goes to stderr, and in json and yaml mode it is held back, its last line
  ending the error's message when sftp fails.
- Every command that changes state and has no API resource to return
  carries `action` and `ok`, plus `name` (or the identity fields, such as
  `group` and `id`) when it acts on a named resource:
  `{"name":"my-app","action":"deleted","ok":true}`. That includes the
  commands that write the config file or a key (`login`, `logout`, `use`,
  `config set`/`unset`, `ai-gateway login`/`logout`, `ssh-key setup`,
  `agent init`), beside their own fields. `ok` is always `true`: a failure
  exits non-zero with the error document on stderr instead, so check the
  exit code, not `ok`.
- `saladcloud --version` is `saladcloud version`, so `--version -o json`
  returns the same document.
- `-o yaml` renders the same document as `-o json` (every key filled the
  same way), through `sigs.k8s.io/yaml`; `logs -o yaml` is a YAML stream,
  one document per entry opened with `---` and flushed as it arrives.
  Errors stay the one-line JSON document on stderr in yaml mode too.
- Every key is always present. An array or map the API omitted renders
  empty (`[]`, `{}`); `null` means the value is unknown or not set (a
  `project` that none was set for reads `null`).
  Adding a key is not a breaking change; removing or renaming one is, and
  is listed in the changelog.
- `env_overrides`, on every command that writes the config file and on
  `doctor`, lists exactly the variables its `warning:` names: those set to
  something other than what the command saved (for `doctor`, other than
  the saved value), or, for a value the command removed (`logout`, `config
  unset`), those set at all. It is `[]` when none is.
- Keys are snake_case, with two exceptions the API spells in kebab-case on
  a container group: `scaling-actions` and `scheduled-scaling-enabled`. In
  jq they need the bracket form: `.["scaling-actions"]`.
- Strings are not HTML-escaped: a message's `<group>` reads `<group>`.
- JSON output is ASCII: every non-ASCII character (an organization's display
  name, an environment value, a transcript) is written as a `\uXXXX`
  escape, which every JSON parser reads back as the same character. Windows
  PowerShell decodes a command's output in the console's code page, which
  would otherwise garble it. YAML output keeps the characters as they are.
- In json and yaml mode, errors are a single-line JSON object on stderr:
  `{"error":{"exit_code":4,"kind":"not_found","message":"...","hint":"...","http_status":404}}`.
  `message` states the problem and `hint` the fix, `null` when there is
  none; act on the hint. `kind` is one of the [error kinds](#error-kinds);
  `http_status` is `null` when no HTTP response caused the error.
  Command-line mistakes count: an unknown command (with its did-you-mean), a
  missing argument, or a missing or unknown flag anywhere on the line
  reports as JSON when the line carries a valid `-o json` (or `-o yaml`).
  Only an invalid `-o` value reports in table form, because it cannot select
  a mode.
- In json and yaml mode, stderr carries nothing but `warning:` and `note:`
  lines and, when the command fails, the error document as its last line;
  everything a table-mode run says on stderr besides those is in the JSON
  document. Prompts appear on the terminal itself, not on stderr (only
  when the terminal device cannot be opened do they fall back to stderr).
  `--debug` adds its own lines, and an interactive `container-groups ssh`
  or `sftp` session's stderr is the remote's.
- Table mode errors are `saladcloud: error: <message>` on stderr, then
  `hint: <fix>` on the next line when there is one.
