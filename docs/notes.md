# Notes

Things about the CLI and the SaladCloud API that might surprise you. Command names such as `create` and `delete` below
are the `saladcloud container-groups` commands.

## How the CLI behaves

- **Write warnings never show a secret.** When `update` or `scale` reports what the API changed or ignored, an
  environment variable's value, a header's value or a credential field is named and described as `(set)`,
  `(changed)`, `(removed)` or `(hidden)`, never printed, as `--debug=body` redacts them.
- **`delete` is immediate and unconfirmed.** There is no prompt and no dry run, so it works unattended in scripts and
  agents. Confirm before you run it, and guard destructive automation in your own code.
- **`update` and `scale` wait for the change to settle** before they return, so they can check the result against what
  you asked for and warn on stderr when the API changed or ignored part of it. `--no-wait` returns at once and skips
  the check.
- **The project falls back to `default`** when nothing sets one, and every command that uses it warns on stderr;
  `whoami` shows it as `none set, using "default"`. A saved project applies only to the saved organization. If the
  organization has no `default` project, commands exit 2 with "no valid project set". See
  [Configuration](guide.md#configuration).
- **`whoami`'s `authenticated` is `true`, `false`, or `null`**: `true` when the API accepted the key, `false` when
  it rejected it, and `null` when there was nothing to check (no key, or neither the list of your organizations nor
  an organization to check the key against). It is checked through that list, or the organization when the list is
  unavailable; `organization_found` is the organization check, `null` when it was not made.
- **The list of your organizations comes from an endpoint outside the documented API**, so it may change or go
  away without notice. `orgs list` and `orgs get` read it, as do login's numbered list, the names in setup hints and
  `whoami`'s `organizations` row. When it is unavailable, `login` asks you to type the organization's name instead
  of numbering them, `login`, `use` and `doctor` check the organization you name directly (that check is in the
  documented API), hints say to run `saladcloud orgs list` instead of listing names, and `whoami` shows
  `organizations` as `unknown` (`null` in JSON) and checks the key against the organization. Every other command
  works without it.
- **Empty table listings keep their header row** on stdout, so `cut`- or `awk`-style pipelines always see the same
  shape; a note on stderr says nothing matched.

## Platform behavior to know about

- **Deleted container group names are never released.** Names are unique within an organization and project,
  including against deleted groups, so re-using a name returns `name_conflict`. Pick a new name.
- **A group created with autostart can come up `stopped`, with no reason given.** The create response says `pending`,
  and about 20 seconds later the group reads `stopped`; `container-groups start <group>` starts it.
  `create --wait` reports it (kind `group_stopped`); a plain `create` returns before it happens.
- **A new group can stay `pending` for a long time** while SaladCloud pulls its image, longer for a large image or
  when the platform is busy, with nothing logged yet; it then moves to `deploying`. If it stays `pending` for a very
  long time, contact SaladCloud support.
- **A group reads `running` once any instance's container runs, ready or not.** The group's counts don't include
  readiness, so a service whose readiness probe never passes still shows `running` in `get` and `list`; `instances
  list` shows each instance's READY, and a wait for `running` prints a note when some aren't. The group's counts can
  also trail `instances list` by a few seconds.
- **A large image can take a while to start** on a node that has not pulled it before: instances sit in `Downloading`
  for longer the bigger the image is.
- **The gateway can take a short while to start routing to a new instance.** Right after an instance first reads
  ready, requests to the group's URL can get 503 for a minute or so.
- **Changing the image, command, environment, probes or gateway settings restarts every instance** onto the new
  version as its node next checks in; old and new never run side by side. For no downtime, run the new version as a
  second group (see [Updating with flags](guide.md#updating-with-flags)).
- **Setting `replicas` puts scheduled scaling on hold**, whether through `scale`, `update`, or the portal. The CLI says
  so when it happens; `scale --keep-schedule` switches the schedule back on. See
  [Scheduled scaling](guide.md#scheduled-scaling).
- **A running or deploying GPU group can lower its priority only after 60 minutes at its current one.** Until then,
  `update --priority` with a lower tier fails with exit 6 (kind `invalid_input`) and the API's message saying so. A
  stopped group's priority changes at once, so to lower it now, `stop` the group, `update --priority`, then `start` it.
- **Job queues** (see [Job queues](guide.md#job-queues)):
  - `create` accepts `queue_autoscaler` and drops it: the group comes back with `queue_autoscaler: null`, and
    `create` warns on stderr. Set it with `container-groups update -f` once the group exists.
  - Setting `queue_autoscaler` also changes `replicas` (1 to 0 with `min_replicas` 0); `update` warns.
  - `queues get` lists no container groups: `container_groups` is `[]` even while a connected group is running, so
    the GROUPS column of `queues list` shows `-` for an empty list.
  - Cancelling a running job marks it `cancelled` and discards its output, but the instance still runs it to the end.
  - A stopped or deleted group's worker can take new jobs for some seconds afterwards. Such a job, like one whose
    instance was interrupted, reads `running` until its lease runs out (about 5.5 minutes) and a worker next asks
    for work; with no worker left on the queue, it stays `running`.
  - Deleting a queue deletes its pending jobs and removes the connection from every group connected to it, for good;
    a new queue of the same name does not reconnect them.
  - A reply over 10 MB counts as a failed attempt, so the job runs four times and then fails with no reason given.
  - The job queue worker logs only errors by default, so a failed attempt leaves no line in the logs. Its
    [README](https://github.com/SaladTechnologies/salad-cloud-job-queue-worker#configuration) names the
    environment variable that raises the level; at `info` it logs each job, full input included.
- **An invalid `container.command` never settles.** The instance fails at start and the platform moves it to another
  node, over and over: the group never reaches `running`, each attempt's logs stop after "Instance Starting", and
  `--wait` times out. A common cause is busybox's `sleep`, which takes a number of seconds, not `infinity`; the examples
  use `sleep 2147483647`. The platform also drops empty words from `container.command` and trims the spaces around each
  word, so `create`/`update -- <command>` refuses an empty word.
- **SSH has a few relay rules.** Each account registers exactly one public key (ssh-ed25519 or ssh-rsa) in the portal,
  at https://portal.salad.com/ssh-keys, and registering one replaces any other; `saladcloud ssh-key setup` finds or
  generates a local key and prints it with that link. A one-shot `container-groups ssh <group> -- <cmd>` will exit 1 even
  when the command succeeded, because the relay does not pass exit codes back (the CLI prints a note; `cp`, `sftp` and
  interactive sessions are unaffected). Its output also comes through a terminal, so stderr is mixed into stdout and
  programs may add color; move files with `cp`. An instance can read `running` a minute or so before the platform
  assigns its SSH endpoint (the SSH column of `instances list`); until then `ssh` says so. `--user` must exist in the
  image; `root` always works. Shell syntax such as
  `&&` works in one-shot commands. The CLI keeps relay host keys in its own `known_hosts` beside the config file, filed
  under each instance's ID rather than the relay's address, so a relay port another instance reused neither conflicts
  with your `~/.ssh/known_hosts` nor fails the host-key check.
- **`cp -r` follows sftp's directory rule.** When the destination directory already exists the source is copied inside
  it (`cp -r ./input my-app:/data` makes `/data/input`); when it does not, it is created as the copy. End a remote path
  with `/.` to download only its contents (`cp -r my-app:/outputs/. ./results`).
- **`logs --follow` can miss entries that arrive very late.** The API can only search by when an entry was emitted,
  not when it was stored, so `--follow` re-checks a window reaching `--lookback` (default 2m) behind the newest entry
  it has seen. Raise `--lookback` for workloads whose logs reach SaladCloud with a long delay.
- **Log time ranges have millisecond precision.** Finer `--since`/`--until` values are rounded outward automatically.
- **GPU availability can read 0 when the availability service is overloaded**: it then answers with every count at 0
  instead of an error, so an overloaded class looks like an empty one. The on-call count (`on_call_gpu`,
  `on_call_cpu` in JSON) is 0 for every row today and is no tell; the table leaves its `ON-CALL` column out until a
  row reports one. `availability` prints a stderr `warning:` for each row
  whose every priority-tier count is 0. Treat those counts as unknown and run `availability` again.
- **One availability query for several GPU classes is its own answer, not the sum of the per-class queries.** It is
  taken separately, so it neither equals nor bounds their sum: a node that carries more than one of the classes counts
  once in it, and the counts move between queries. `availability` shows
  one row per class plus the combined query's own answer, never a total of the rows.
- **The availability service sometimes answers all zeros between two good answers.** `availability` asks once more when
  every count is zero; a row still all zeros gets the stderr warning marking it unknown.
- **Some responses carry `restart_policy` or `priority` as a number** instead of its name: 1, 2, 3 for `always`,
  `on_failure`, `never`, and 1 to 4 for `high`, `medium`, `low`, `batch`. The CLI rewrites a known number to its name
  and drops one with no name (such as 0), so that field reads `null` rather than failing the whole command.
- **Queue job listings page at 25.** The API rejects `page_size` above 25 for queue jobs (the API reference allows
  100), as it does for inference endpoints and jobs.
- **Job listings come in no useful order and with no total.** Inference jobs are listed oldest first and queue jobs in
  job ID order, with no sort option and a null `total_size`. The CLI shows both newest first: it searches for the last
  page of an inference listing and reads backwards. A queue listing has to be read and sorted; with a `--limit`
  (100 by default) `queues jobs list` reads at most 1,000 jobs (or the limit, if larger), and says on stderr when
  the queue may hold more than it read, since the newest are then the newest of those read. `--limit 0` reads every
  job, up to 10,000, which takes a while on a long queue.
