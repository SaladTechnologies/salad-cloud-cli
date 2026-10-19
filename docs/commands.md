# Commands

Every command has `--help` with its full flags and examples.

The grammar is the same everywhere: resource nouns take a verb (`gpus list`, `orgs get <name>`), the thing a verb
acts on is its one positional argument, and every parent above it is a named flag
(`container-groups instances restart <id> -c <group>`, `inference jobs get <id> --endpoint <endpoint>`). A noun run
without a verb is a usage error (exit 2) that lists its verbs; `--help` prints the help. Query commands (`logs`,
`availability`) act directly and take their filters as flags. An unknown command name gets a hint listing up to
four commands of that name elsewhere in the tree, a noun with the verb you typed after it (`saladcloud jobs list`
suggests `inference jobs list` and `queues jobs list`), the command a common guess means (`status` suggests
`whoami`), or the nearest spelling of a typo (`cg lst`), on one line. A command line in the wrong shape gets the
command to type as its hint: a parent given as an argument (`instances list my-app` → `instances list -c my-app`),
`-c` on `ssh`, a missing argument or required flag with the usage line; a flag value of the wrong type says what it
should be.

Global flags: `-o table|json|yaml`, `--org`, `--project`, and `--debug` (`--debug=body` for headers and bodies,
credentials redacted). A command shows and accepts `--org` and `--project` only when it reads them; given anyway,
either is an unknown flag (exit 2). The organization-scoped commands (`inference`, `quotas`, `gpus`, `webhook-secret`)
take `--org` but no `--project`; `agent`, `ai-gateway`, `changelog`, `completion`, `config`, `logout`, `orgs`,
`ssh-key`, `use` and `version` take neither. On `logs` only an explicit `--project` narrows the query.
Tables show times in UTC to the second (`2026-10-03 14:37:17Z`); `-o json` keeps the API's own timestamps.

## Aliases

| Command | Also answers to |
| --- | --- |
| `container-groups` | `container-group`, `cg` |
| `container-groups instances` | `instance` |
| `orgs` | `org`, `organizations` |
| `gpus` | `gpu`, `gpu-classes` |
| `queues` | `queue` |
| `queues jobs`, `inference jobs` | `job` |
| `ai-gateway models` | `model` |

Every plural resource noun answers to its singular, and the table is every alias there is. Singletons (`quotas`,
`webhook-secret`, `ssh-key`) keep one spelling.

## Setup and account

| Command | Description |
| --- | --- |
| `login` | Save your API key, organization and project, each checked against the API; at a terminal it asks (numbering your organizations, offering what is saved), without one it takes `--api-key -`, `--api-key` or `SALAD_API_KEY` and `--org`; a missing project never fails it |
| `logout` | Remove the saved API key; the organization and project stay (and it says which); warns when `SALAD_API_KEY` still supplies one |
| `use <org>[/<project>]` | Switch the saved organization and project, checked against the API; `use <org>` alone selects the organization's `default` project, or saves the organization with no project when it has none; warns when `SALAD_ORGANIZATION` or `SALAD_PROJECT` would be used instead |
| `whoami` | Show the API key, organization and project in use, where each came from, the saved value a variable hides, whether the API accepted the key, and your organizations; never fails over a missing or rejected key |
| `doctor` | Check the config file, the API, the API key, organization and project, and whether an AI Gateway key and a local SSH key exist; each row says what it found, a hint says what to do, and a failing run names the failed checks (`-o json`: `{"items": [...], "env_overrides": [...]}`, one item per check, each with `hint`) |
| `config set/get/unset/list/path` | Read or change the config file, which holds your SaladCloud API key (`api-key`), the default `organization` and `project`, and your AI Gateway key (`ai-gateway-api-key`); `list` masks both keys to their last four characters, `set api-key -` reads the key from stdin, `path` prints where the file is; `set` and `unset` warn when a variable would still be used instead |
| `orgs list` / `orgs get <name>` | List your organizations, or show one (uses an endpoint that is not part of the documented API and may change; see [Notes](notes.md#how-the-cli-behaves)) |
| `quotas get` | Show the organization's container group quotas |
| `ssh-key setup` / `ssh-key get` | Find or generate a local SSH key and show where to register it (one key per account, at https://portal.salad.com/ssh-keys) / print the local key the CLI offers; whether it is the one registered on the account cannot be checked from the CLI. With `-o json`, `has_passphrase` says whether the key has a passphrase, which `cp` and one-shot `ssh` cannot ask for (`null` when it cannot be told) |
| `webhook-secret get` / `webhook-secret rotate` | Print the key the organization's webhook deliveries are signed with / replace it and print the new one |

## GPUs and availability

| Command | Description |
| --- | --- |
| `gpus list` | List GPU classes and their prices per priority tier (`--all-prices` adds every price entry) |
| `gpus get <class>` | Show one GPU class, by UUID or name, with every price entry |
| `availability [--gpu <class>]... [--cpu <n>] [--memory <mb>] [--storage <gb>] [--country <cc>]... [--from <group>]` | How many nodes could take one replica with these requirements right now, one row per GPU class plus a combined row for several (CPU-only without `--gpu`); each class is one platform query, plus one for all of them, so call it once before `create`, not in a loop (see [Availability](guide.md#availability)) |

## Container groups

Under `saladcloud container-groups`; `saladcloud cg list` works too.

| Command | Description |
| --- | --- |
| `container-groups create [<group>] [-- <command>...]` | Create a container group from flags, a `-f` definition file, or a copy of an existing group (`--from`); words after `--` set the container command; `--wait` blocks until it is running; `--queue`, `--queue-port`, `--queue-path` connect it to a job queue (with a note on stderr that the image must run the job queue worker) |
| `container-groups list` | List the container groups in the current project, with running instances against replicas (`RUNNING 1/2`) and the image (`--status <s>` filters, `-w` watches; `--projects a --projects b` lists several) |
| `container-groups get <group>` | Show one container group, with the platform's reason when it failed or stopped (`-w` watches; `--writable` emits a JSON patch document that `update -f` accepts) |
| `container-groups update <group> [-f <file>] [flags] [-- <command>...]` | Update a container group from flags (`--image`, `--cpu`, `--memory`, `--storage`, `--gpu`, `--country`, `--replicas`, `--priority`, `--port`, `--display-name`, `-e`, the container command after `--`) and/or a JSON/YAML merge patch (see [Updating with flags](guide.md#updating-with-flags)); waits for the change to settle and verifies it applied — `--no-wait` returns at once |
| `container-groups start <group>` / `stop <group>` | Start / stop a container group; `--wait` blocks until running / stopped |
| `container-groups delete <group>` | Delete a container group (immediate and unconfirmed; its name is never released) |
| `container-groups scale <group> --replicas <n>` | Set the replica count (same write path as `update`: settles and reports side effects; puts scheduled scaling on hold — `--keep-schedule` switches it back on) |
| `container-groups instances list -c <group>` | List a group's instances |
| `container-groups instances get/restart/recreate/reallocate <id> -c <group>` | Show or act on one instance (the ID may be a unique prefix) |
| `container-groups instances update <id> -c <group> --deletion-cost <n>` | Set an instance's deletion cost (lower is removed first on scale-down) |
| `container-groups logs <group> [--instance <id>]` | One container group's logs; `-f` on a failed or stopped group says it will stay silent |
| `container-groups ssh <group> [--instance <id>] [-- <cmd>]` | SSH session (or one-shot command, or `--config` Host blocks) on an instance; `--user` picks the container user (default `root`), `-i/--identity` the private key |
| `container-groups cp <src> <dst>` | Copy files to/from an instance over SFTP (`<group>[/<instance>]:<path>`); `-r` copies directories, `--user` and `-i` as on `ssh`; stdout carries one result; sftp's output goes to stderr in table mode and is held back with `-o json`/`-o yaml` (its last line ends the error when sftp fails) (`{"group","instance","action":"copied","ok","src","dst"}` with `-o json`). In Git Bash, put `MSYS_NO_PATHCONV=1` in front of a `<group>/<instance>:<path>` argument, which Git Bash otherwise rewrites as a Windows path list |
| `container-groups sftp <group> [--instance <id>]` | Open an interactive SFTP session on an instance |

`ssh`, `cp`, and `sftp` pick the group's first running instance when you do not name one, and say which on stderr
(`note: using instance <id>`).

## Logs

| Command | Description |
| --- | --- |
| `logs [-c <group>] [--project <p>] [--instance <id>]` | Query or follow (`-f`) logs and system events for the whole organization, one project (`--project`), or one container group (`-c`); table mode names the scope on stderr and shows a system event as its message; `--poll-interval` paces `--follow`, `--page-size` (1-100) sets entries per request (see [Logs](guide.md#logs)) |

## Job queues

| Command | Description |
| --- | --- |
| `queues create <name> [--display-name] [--description]` | Create a job queue in the current project |
| `queues list` / `queues get <name>` | List the project's queues / show one (length and connected groups) |
| `queues update <name> [--display-name] [--description]` | Change a queue's display name or description |
| `queues delete <name>` | Delete a queue and its pending jobs; groups connected to it lose the connection for good |
| `queues jobs create --queue <q> (-f <file> \| --input '<json>') [--webhook <url>]` | Submit a job; `--wait` blocks until it finishes |
| `queues jobs list --queue <q> [--limit <n>]` / `get <id> --queue <q>` | List a queue's jobs, newest first (`--limit`, 100 by default, reads at most 1,000 jobs and says when the queue may hold more; `0` reads all, up to 10,000) / show one (the full job UUID) |
| `queues jobs cancel <id> --queue <q>` | Cancel a pending or running job; a running job is marked canceled, but its instance still runs it to the end |

## Inference and AI Gateway

| Command | Description |
| --- | --- |
| `inference list` / `inference get <endpoint>` | List the org's managed inference endpoints / show one, with its price and input fields |
| `inference jobs list/get/create/cancel ... --endpoint <endpoint>` | Queue and inspect jobs on any inference endpoint; `list` is newest first (`--limit` keeps the newest: 100 by default, `0` for all up to 10,000), `get` and `cancel` take the full job UUID, `create --wait` blocks until the job finishes |
| `inference transcribe <file-or-url>` | Transcribe an audio or video file or URL in one step on the `transcribe` endpoint: uploads local files to S4, queues the job, waits, and prints the transcript; one flag per input field, and a field that is a list (`--llm-translation`, `--srt-translation`, `--custom-vocabulary`, `--classification-labels`) is repeated, one value each |
| `inference transcription-lite <file-or-url>` | The same on the cheaper `transcription-lite` endpoint, with its own (smaller) set of input fields |
| `ai-gateway login` | Save an AI Gateway key, checked with one billed request: asked for at a terminal, or `--api-key -` (stdin), `--api-key`, or `SALAD_AI_GATEWAY_API_KEY` (checked, not saved) (see [AI Gateway](guide.md#ai-gateway)) |
| `ai-gateway logout` | Remove the saved AI Gateway key; warns when `SALAD_AI_GATEWAY_API_KEY` still supplies one |
| `ai-gateway models list` | List the models the AI Gateway serves (public: no key needed) |
| `ai-gateway test [--prompt ...]` | Send one completion to verify the gateway key; prints the reply (`--model` picks the model, default the first id in sorted order; `--max-tokens` the budget) |

## Other

| Command | Description |
| --- | --- |
| `version` | Print the CLI version (`saladcloud --version` is the same) |
| `help [<command>]` | Print a command's help, the same as `saladcloud <command> --help` |
| `completion bash\|zsh\|fish\|powershell` | Print the shell completion script; it completes container group, instance, queue, endpoint, GPU class and organization names, and enum flag values, with a quiet 3-second lookup that suggests nothing when it cannot reach the API |
| `changelog [--since <date\|version>]` | Print the embedded CHANGELOG.md; `--since` limits it to releases newer than a date or version (Unreleased always included) |
| `agent init` | Write the marker-delimited SaladCloud block into the project's `AGENTS.md` (or `CLAUDE.md` under Claude Code); re-runnable, `--print` to preview, `--dir` for another project (see [Coding agents](guide.md#coding-agents)) |
