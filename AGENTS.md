# Guidance for coding agents working in this repository

This is the `saladcloud` CLI: a thin, non-interactive-first Go wrapper over the SaladCloud Public API, built for
scripts, CI, and AI agents. Read this file before changing anything. `README.md` covers install and getting started;
`docs/` is the user-facing reference and contract; `CHANGELOG.md` records every user-visible change. The settled design
choices and their reasons are under [Settled decisions](#settled-decisions) below — check there before reopening one.

## Build, test, verify

```sh
mise install      # the tools at the versions in mise.toml: Go, golangci-lint, govulncheck, go-licenses, just, Node,
                  # PowerShell, ShellCheck, shfmt
npm ci            # prettier and cspell, at the versions in package-lock.json
just build        # cross-compiles into dist/ (windows-amd64, linux-amd64, darwin-arm64, darwin-amd64)
just test         # offline suite, shuffled, with -race where cgo is available
just vet          # go vet ./...
just fmt          # golangci-lint fmt (gci, gofumpt, golines), PSScriptAnalyzer, shfmt, prettier
just lint         # golangci-lint, go mod tidy -diff and verify, govulncheck, go-licenses, PSScriptAnalyzer,
                  # ShellCheck, cspell
just ci           # fmt-check, lint and test, as CI runs them
just test-live    # live suite against the real API; needs SALAD_API_KEY and SALAD_ORGANIZATION (SALAD_PROJECT optional:
                  # default when unset), spends money, fails without them. Optional: SALAD_CLI_TEST_REQUIRE_SSH=1 fails
                  # the ssh test instead of skipping it on a publickey denial, SALAD_AI_GATEWAY_API_KEY adds the AI
                  # Gateway test, SALAD_CLI_TEST_AUDIO=<short wav> and SALAD_CLI_TEST_PHRASE=<words in it> add
                  # transcription, SALAD_CLI_TEST_AUDIO_FOREIGN=<short single-track non-English clip> adds the
                  # language_code and audio_stream_index test, SALAD_CLI_TEST_BIN=<path> tests another binary than
                  # dist/.
```

A change is done when `just ci` passes, `docs/` and the CHANGELOG reflect it, and anything that touches an API call
has been exercised live (or the pull request says it has not). Lint settings live in `.golangci.yml`,
`.prettierrc.yaml` and `.prettierignore` (`AGENTS.md` and `docs/` keep their hand-wrapped compact tables),
`.shellcheckrc`, `PSScriptAnalyzerSettings.psd1` and `cspell.json`; add a project term to
`dictionaries/salad-cloud-cli.txt`, and a deliberate misspelling in a test to `dictionaries/test-fixtures.txt`.

Format with `just fmt` after editing (or one language's recipe: `fmt-go`, `fmt-powershell`, `fmt-shell`,
`fmt-markup`), and check with the matching `fmt-check-*` and `lint-*` recipes and `just test`. Never run `gofmt`,
`goimports` or `go fmt` directly (they disagree with `.golangci.yml`), and never run a formatter or linter with flags
or settings that override the configuration. Every finding is fixed; a suppression names its rule and its reason, on
the narrowest scope there is: in Go, `//nolint:<linter> // <reason>` on the line (never a bare `//nolint`, a file-wide
directive, or a `.golangci.yml` exclusion); in PowerShell, a
`[Diagnostics.CodeAnalysis.SuppressMessageAttribute('<rule>', '<target>', Justification = '<reason>')]` on the
narrowest function or `param` block (never script-wide, never a `PSScriptAnalyzerSettings.psd1` exclusion); in shell,
`# shellcheck disable=SC<n> # <reason>` on the line before the one command it covers (never file-wide, never a
`.shellcheckrc` exclusion). Add a Go dependency with `go get` then `go mod tidy`, and commit `go.mod` and `go.sum`
together; its license must be on the justfile's `go-allowed-licenses`. Developer tools go in `mise.toml`, never a
`tool` directive.
CI (`.github/workflows/test.yml`) installs the `mise.toml` tools through `.github/actions/setup-mise`, which fetches
mise at a pinned, checksum-verified version (the organization's allowed-actions list does not include
`jdx/mise-action`), and runs `just vet`, `just test` and `just build` on Linux, macOS, and Windows, and
`just fmt-check lint` on Linux; it does not run the live suite by decision. It runs on every push and pull request,
and by hand with `gh workflow run test.yml` (`--ref <branch>` for another branch). Actions are pinned by commit SHA
and Dependabot bumps them; the mise version in `setup.sh` is bumped by hand, with each archive's sum from the
release's `SHASUMS256.txt`.

Releasing. Versions follow SemVer and are tagged `vMAJOR.MINOR.PATCH`. The CLI stays on major version 0 while it is in
beta (SemVer's 0.y.z, where anything may change): a release with a new feature or any breaking change bumps MINOR, one
with only fixes bumps PATCH, and leaving the beta is `v1.0.0` (v2 and later would need a `/v2` module path). The one
suffix is `-rc.N` (`v0.2.0-rc.1`), a release candidate published to try a release before calling it done. A tag goes
only on a commit on `main` that passed `test.yml`, is annotated, and is permanent: the tag rulesets let nobody move or
delete one, so a bad release is followed by the next version rather than a new tag of the same one, and `release.yml`
refuses a version of another form or a commit that is not on `main`. There are no maintenance branches: fixes land on
`main` and ship in the next version.
To release, merge a PR that turns `[Unreleased]` into `## [X.Y.Z] - <today>` (`release.yml` fails without that section
and warns on another date or a non-empty Unreleased), run `just test-live` with `SALAD_CLI_TEST_REQUIRE_SSH=1`, using
the Windows build from a machine whose SSH key is registered on the account (so ssh and cp really run), open one `sftp`
session by hand, then start `.github/workflows/release.yml` on `main` (Actions → Release → Run workflow, or
`gh workflow run release.yml --ref main -f version=vX.Y.Z`, with `-f commit=<sha>` when `main` has moved past the
tested commit). Its `verify` job checks, with no secrets and a read-only token, the version's form, that the commit is
on `main` with `test.yml` green and no GitHub release yet, that the binary's `version` matches, and that CHANGELOG.md
has a dated section for it. `publish` then waits for someone in `engineering` or `salad-cloud-cli-maintainers` (whoever
started it may) to approve the `release` environment, and creates the tag with the release GitHub App's token: the tag
rulesets let only the App create one. Until the App and the `release` environment are set up, `just release vX.Y.Z` is
the way in: it checks the version, that the tag is new, the CHANGELOG section and `test.yml` on `main`'s tip, then
creates and pushes the tag, and the push runs the same workflow, which then has no tag to create. That path closes once
the tag ruleset lets only the App create tags; then the push trigger and `scripts/release.sh` go. A tag the App pushes
starts no second run. A release that fails in the workflow rather than in the tagged code is published again from the
same tag: fix the workflow on `main`, then run it again with the same version, which finds the tag and builds that
tag's code with `main`'s workflow (re-running the failed run would reuse the workflow as it was at the tag). It refuses
a version that already has a GitHub release, so delete a half-made one first (the tag stays), which also keeps the
installers on the release before. A fault in the tagged code is fixed forward in the next version.
goreleaser (`.goreleaser.yaml`) builds reproducible archives for linux and darwin amd64/arm64 and windows amd64 (each
with README, LICENSE, CHANGELOG, `docs/` and `completions/`), `checksums.txt`, SBOMs, the
`ghcr.io/saladtechnologies/saladcloud` image (also tagged `latest` unless it is a pre-release), and a GitHub release (a
pre-release when the tag has a `-`), followed by a build provenance attestation for every archive. The Homebrew cask,
Scoop manifest and winget pull request publish only for releases that are not pre-releases, and only when
`HOMEBREW_TAP_TOKEN`, `SCOOP_BUCKET_TOKEN` and `WINGET_TOKEN` are set as secrets of the repository or the `release`
environment (and their `SaladTechnologies/homebrew-tap`, `scoop-bucket` and `winget-pkgs` fork exist). `install.sh`
and `install.ps1` at the repo root install from those releases; they depend on the archive names and the
`checksums.txt` format goreleaser produces, and `test.yml` runs both against archives goreleaser packages from the
commit (and against the latest published release, once there is one). `uninstall.sh` and `uninstall.ps1` undo them (the
binary, the Windows user `PATH` entry, and with `SALAD_CLI_PURGE=1` the config file), and `test.yml` runs them after
each install. The CLI is in beta: `internal/version.Stage` drives the notice in `--help`, `version`, and the `agent
init` block, and the release notes carry a beta notice for every 0.x release; drop `Stage` and release `v1.0.0` when
the CLI leaves beta.

## Layout

- `cmd/saladcloud/main.go` — entry point: creates the root context (canceled on Ctrl-C or SIGTERM), runs
  `cli.Run(ctx, cli.ProcessDeps())` and exits with its code; nothing else.
- `internal/cli/` — one file per verb (`create.go`, `logs.go`, `agent.go`, …), each with an `addXxxCommands` method
  that builds its commands for one run; `New` builds the whole tree from them, so nothing is registered at package
  load. `app.go` holds `Deps`, everything a run takes from the process (arguments, streams, the environment as a
  `config.Env`, the terminal, and the seams tests replace: the sftp program, ssh-keygen, the child environment), and
  `App`, one run: its deps, the output printer, and the flags every command reads. `root.go` owns the global flags,
  `Run` with the error-to-exit-code mapping, and `NewClient`; `orgScoped` and `contextFree` keep `--project` (and
  `--org`) off commands that never read them. The setup helpers every verb uses, all `*App` methods: `console.go`
  (`atTerminal()`, which counts `SALAD_CLI_NO_PROMPT` and `CI`, and the hidden read), `prompt.go` (`ask` and `tell`,
  which write to the terminal device, falling back to stderr only when it cannot be opened), `setup.go` (`errw()`,
  `say` and `hintLine` for stderr status and `hint:` lines, which print in table mode only; `maskAPIKey`, `fromTag`,
  `checkName`) and `envwarn.go` (`warnOverrides` and `env_overrides` for every command that writes the config
  file).
- `internal/api/` — the CLI's own HTTP transport (`rest.go`) plus per-area calls. The generated SDK
  (`salad-cloud-sdk-go`) is used **only for its types**; its transport rejects real API responses (7-digit fractional
  timestamps, JSON nulls). Do not route requests through the SDK client.
- `internal/clierr/` — exit codes and the structured error. `internal/output/` — the single render path (`Emit`,
  `Stream`). `internal/config/` — flag > env > config-file resolution; a saved project applies only to the saved
  organization, and `default` is the project's last fallback (warned on stderr). `internal/input/` — definition-file
  loading and strict unknown-field checking. `internal/logsq/` — log query windowing, pagination, follow.
  `internal/agentdoc/` — the block `agent init` writes. `internal/livetest/` — the live suite (build tag `live`).

## Writing code

Go:

- Return a `*clierr.Error` from command code and wrap other errors with `%w`, matching them with
  `errors.Is`/`errors.As`. Never `panic`; only `main` calls `os.Exit` or creates a root context.
- No package state: a run's state lives on its `App`, constant tables are functions, and only regexps, sentinel errors
  and the ldflags-stamped version variables are package variables. The environment is read through `config.Env`,
  never `os.Getenv`.
- Diagnostic logging, should the CLI ever add any, goes through `log/slog` and a passed-in `*slog.Logger` and its
  `...Context` methods: attributes only, snake_case keys, lowercase constant messages. Results, `warning:`, `note:`
  and `hint:` lines, the error document and `--debug` dumps are not logging and keep their own paths.
- Godoc every package and exported identifier. Wrap comments by hand at 120 columns; the formatter wraps code only.
- Tests use the standard `testing` package, are table-driven where they repeat, use `t.Context()`, and call
  `t.Parallel()` in every test and subtest; none calls `t.Setenv`. A live test that creates resources on the account
  runs alone, under a `//nolint:paralleltest` with that reason.

PowerShell (scripts run with pwsh):

- A script that is run directly starts with `#!/usr/bin/env pwsh` and is committed as executable
  (`git update-index --chmod=+x -- <file>`, which also works on Windows).
- Scripts begin, after any shebang, with `[CmdletBinding(SupportsShouldProcess = $true)] param()`,
  `#Requires -Version 7.4`, `Set-StrictMode -Version Latest`, `$ErrorActionPreference = 'Stop'`, and
  `$PSNativeCommandUseErrorActionPreference = $true`. Functions use the same `[CmdletBinding()]`, minus
  `SupportsShouldProcess` if read-only. Prefer cmdlets and guard each change with `$PSCmdlet.ShouldProcess()` so
  `-WhatIf` works.
- The installers, `install.ps1` and `uninstall.ps1`, are exempt from the shebang, `#Requires -Version 7.4` and
  top-level `param()` and settings: they must run on Windows PowerShell 5.1 under `irm | iex`, where anything set at
  the top level leaks into the user's session, so they keep their settings inside their one function.

Shell:

- A script that is run directly starts with a shebang and is committed as executable
  (`git update-index --chmod=+x -- <file>`). A file that is only `source`d has no shebang and starts with a
  `# shellcheck shell=bash` directive instead. The installers, `install.sh` and `uninstall.sh`, stay POSIX `sh`.

## Contracts you must not break

These are public. Scripts and agents depend on them; `docs/guide.md` documents exit codes and the JSON
contract. During the beta none of them is promised: any may change, but only on purpose and with a CHANGELOG entry,
never as a side effect of other work.

- **Exit codes are a closed set**: 0 ok, 1 generic, 2 usage, 3 auth, 4 not found, 5 rate limited, 6 invalid input,
  7 network, 10 not available; `ssh`, `sftp` and `cp` exit with their child's code when it fails (kind `child_exit`).
  New failure modes map onto an existing code. For checks the CLI makes before any
  request: **2** when the command line cannot be parsed or is structurally wrong and the CLI alone decides that;
  **6** when it parses but a value breaks a platform rule (one the API would reject with 400/422, or one the platform
  applies after accepting the request, such as a container command it cannot run), or a file the user named cannot be
  read; **3** when there is no credential. Every such check is in the [validation table](#validation-table),
  and `internal/cli/validation_test.go` fails on a check that is not, or whose code disagrees.
- **JSON output shapes are complete.** Every key is always present; an array or map the API omitted renders empty;
  `null` means the value is unknown or not set. `output` fills absent keys after encoding (the SDK tags every field
  `omitempty`), so the CLI's own structs encode what the code set: a nil slice or map is *unknown* and renders `null`,
  an empty one renders `[]`/`{}`, and a scalar that can be unknown is a pointer. Adding a key is fine; removing or
  renaming one is a breaking change and needs a CHANGELOG entry. One JSON document per command on stdout; `logs` emits
  NDJSON, `ssh` and `sftp` hand stdout to the remote session, and `ssh --config`, `completion` and `help` print text.
  Every command that changes state, the API's or the config file's, and has no API resource to return carries `action`
  and `ok`, and `name` (or the identity fields: `group`/`id`, `group`/`instance`, `endpoint`/`id`, `queue`/`id`) when it
  acts on a named resource; `logout` acts on none, so it is `{"removed","organization","project",
  "env_overrides","action","ok"}`. `ok` is always true (failures exit non-zero). `outputkind_test.go` makes every leaf
  command declare its output kind, and `keysnapshot_test.go` compares every emitted key path with
  `testdata/json-keys.golden`, so an SDK bump that moves a key fails CI until the golden file and the CHANGELOG are
  updated.
- **Warnings never print a secret.** A write-verification warning names an environment variable, header or credential
  field and says `(set)`, `(changed)`, `(removed)` or `(hidden)`; the rule is `api.SecretField`, the one
  `--debug=body` redacts by.
- **Errors** are a single-line `{"error":{"exit_code","kind","message","hint","http_status"}}` on stderr in json and
  yaml mode, `saladcloud: error: <message>` then `hint: <fix>` (when there is one) in table mode. The message states the
  problem; the fix goes in the hint (`clierr.Error.WithHint`), `null` when there is none. In json and yaml mode stderr
  carries nothing else but `warning:` and `note:` lines (and `--debug` lines when asked for, prompts when the terminal
  device cannot be opened, and an `ssh` or `sftp` session's own stderr). A valid `-o` anywhere on the command line
  selects the JSON form, command-line mistakes included (an unknown command and its did-you-mean, a bad flag ahead of
  `-o`); only an invalid `-o` value reports in table form. `http_status` is `null` when no HTTP response caused the
  error. `kind` is one of the `clierr.Kind*` constants, listed with their exit codes in `docs/guide.md`. A file the user
  named that cannot be read or parsed is `invalid_input`; I/O on a file the CLI chose (the config file, an AGENTS.md it
  writes) is `api_error`.
- **Non-interactive by default.** No prompts in any verb except `login` (and `ai-gateway login`, `ssh-key setup`); every
  command must work with stdin closed. `delete` is unconfirmed on purpose.
- **Writes verify themselves.** `create`, `update`, and `scale` compare the result with the request (`create` checks
  its response; `update` and `scale` read back) and report drift on stderr as `warning:` / `note:` lines; `update`
  and `scale` settle by default. Keep that path shared (`intentdiff.go`, `writable.go`).
- **Credentials are never printed, logged, or persisted anywhere but the config file**, and always referred to by
  environment-variable name in messages and evidence. Stored credentials are shown masked to their last four characters
  (`****...ABCD`); a key passed as a flag draws a stderr warning, and scripts use the environment variable. The
  exception is a command whose purpose is to return a secret: `webhook-secret get` and `rotate` print `secret_key`
  whole, like `gh auth token`. `--debug=body` redacts the `Salad-Api-Key` and `Authorization` headers, and in JSON
  bodies credential fields (`secret_key`, `api_key`, registry and log-sink passwords and tokens), the value of every
  header in a headers list (log-sink and probe headers), and every environment variable's value; signed S4 URL query
  strings are redacted and a file upload's body is never shown.

## Validation table

Every client-side check in `internal/cli` and its exit code. The last two columns name the field of the error a fragment
is in (`message`, or `hint` for the fix on the line under it) and the fragment, which is how `validation_test.go` ties
each call site to its row; a generic rewrap (`"%v"`) is exempt.

| Input | Exit | Why | Field | Contains |
| --- | --- | --- | --- | --- |
| `-o` value | 2 | the CLI's own flag | message | `invalid --output` |
| `--debug` value | 2 | the CLI's own flag | message | `invalid --debug` |
| `--status` on `cg list` | 2 | a client-side filter | message | `invalid --status` |
| `-e`/`--env` without `=` | 2 | structure | message | `invalid --env` |
| `--interval` below 2s | 2 | the CLI's own minimum | message | `--interval must be at least` |
| `--watch` outside table mode | 2 | flag combination | message | `--watch is table mode only` |
| `--writable` with `--watch` | 2 | flag combination | message | `--writable and --watch cannot be combined` |
| `--timeout` of zero or less | 2 | the CLI's own wait | message | `--timeout must be greater than zero` |
| `--wait` with `--no-autostart` | 2 | flag combination | message | `--wait cannot be combined with --no-autostart` |
| `update --wait-ready` with `--no-wait` | 2 | flag combination | message | `--wait-ready and --no-wait cannot be combined` |
| `update --ready-for` without `--wait-ready` | 2 | flag combination | message | `--ready-for requires --wait-ready` |
| negative `update --ready-for` | 2 | the CLI's own wait | message | `--ready-for must be zero or more` |
| `--no-auth` without `--port` | 2 | flag combination | message | `--no-auth requires --port` |
| `--queue`, `--queue-port`, `--queue-path` not all given | 2 | flag combination | message | `go together` |
| `create` without `--image`, `-f` or `--from` | 2 | missing flag | message | `missing required flag: --image` |
| `scale` without `--replicas` | 2 | missing flag | message | `missing required flag: --replicas` |
| `update --image ""` | 2 | structure | message | `--image cannot be empty` |
| `update` with nothing to change | 2 | missing flag | message | `nothing to update` |
| both or neither of `-f` and `--input` | 2 | flag combination | message | `exactly one of -f <file> or --input` |
| negative `--limit` | 2 | the CLI's own paging | message | `--limit must be` |
| malformed `--from` | 2 | structure | message | `invalid --from` |
| `--from` copy under the source's own name | 2 | structure | message | `give the copy a new name` |
| malformed `cp` remote path | 2 | structure | message | `invalid remote path` |
| `cp` with two remote sides | 2 | structure | message | `both sides are remote` |
| `cp` with no remote side | 2 | structure | message | `no remote side` |
| a `cp` path Git Bash rewrote as a path list | 2 | structure | message | `rewritten by Git Bash` |
| a `cp` path with a line break or another control character | 2 | structure | message | `holds a control character` |
| a `--queue-path` Git Bash rewrote into a Windows path | 2 | structure | message | `turns a leading / into a Windows path` |
| a container command word, or an `ssh` command word after `--`, Git Bash rewrote into a Windows path | 2 | structure | message | `turns a leading / into a Windows path` |
| `create`/`update` with `--` and nothing after it | 2 | missing value | message | `nothing after --` |
| malformed `use` target | 2 | structure | message | `invalid target` |
| unknown config key | 2 | the CLI's own keys | message | `unknown config key` |
| `config set <key> -` with empty stdin | 2 | missing value | message | `value on stdin` |
| `ssh --config` with a `--` command | 2 | flag combination | message | `--config prints Host blocks` |
| `--user` on `ssh`, `sftp` or `cp` that is not a user name | 2 | structure | message | `invalid --user` |
| an identity file path with a control character or a double quote, on `ssh --config` | 2 | structure | message | `cannot be written to ssh_config` |
| a base-URL variable (`SALAD_CLI_API_BASE_URL`, `SALAD_CLI_STORAGE_BASE_URL`, `SALAD_CLI_AI_GATEWAY_BASE_URL`) that is not an https URL, or an http one on localhost | 2 | the CLI's own setting | message | `must be an https URL` |
| `logs --until` with `--follow` | 2 | flag combination | message | `--until cannot be combined with --follow` |
| `logs --until` not after `--since` | 2 | structure | message | `must be after --since` |
| `logs --poll-interval` below 5s | 2 | the CLI's own minimum | message | `--poll-interval must be` |
| `logs --lookback` misuse | 2 | flag combination | message | `--lookback` |
| `logs --page-size` outside 1-100 | 2 | the CLI's own paging | message | `--page-size must be` |
| `logs --instance` prefix without a group | 2 | missing flag | message | `--instance with an ID prefix` |
| `--since`/`--until` unparseable | 2 | structure | message | `invalid time reference` |
| an org-scoped command, or `login` without a terminal, with no organization | 2 | missing flag | message | `no organization set` |
| a project-scoped command with no project | 2 | missing flag | message | `no project set` |
| no project set and the organization has no `default` (any project-scoped command) | 2 | missing value | message | `no valid project set` |
| `logs` with no organization, project or group | 2 | missing flag | message | `is required to query logs` |
| a noun without a verb | 2 | missing argument | message | `needs a verb` |
| a positional missing | 2 | missing argument | message | `missing argument:` |
| a positional in a flag's place (`instances list my-app`) | 2 | structure | message | `not as an argument` |
| a positional too many | 2 | structure | message | `unexpected argument` |
| `-c` on `ssh`, `sftp` or `container-groups logs` | 2 | structure | message | `and the instance as --instance` |
| `-c` on `cp` | 2 | structure | message | `in its remote path` |
| a required flag (`-c`, `--endpoint`, `--queue`) missing | 2 | missing flag | message | `missing required flag: %s` |
| a flag value of the wrong type (`--replicas two`) | 2 | structure | message | `invalid %s "%s": %s` |
| a flag without its value | 2 | missing value | message | `missing value for` |
| `--no-wait` where waiting is opt-in | 2 | the CLI's own flags | message | `unknown flag: --no-wait` |
| `--wait` where waiting is the default | 2 | the CLI's own flags | message | `unknown flag: --wait` |
| any other unknown flag (with the nearest spelling when one is close) | 2 | the CLI's own flags | hint | `to list its flags` |
| an unknown command (the hint lists that name elsewhere in the tree, or the nearest spellings) | 2 | the CLI's own commands | message | `unknown command` |
| `changelog --since` that is not a date or a version | 2 | structure | message | `invalid --since` |
| `ssh-key setup` with no local key, no terminal and no `--generate` | 2 | missing flag | message | `no terminal to ask` |
| group or queue name | 6 | the API's name pattern | hint | `use 2-63 characters` |
| an organization or project name that breaks the API's pattern, from a flag, a variable or `use`'s argument | 6 | the API's name pattern | message | `invalid %s name` |
| an organization or project name that looks like an API key (longer than 63 characters, or a key in use or saved), from a flag, a variable, `use`'s or `config set`'s argument, or the config file; the value is never shown | 6 | the API's name pattern | message | `looks like an API key` |
| `--cpu` | 6 | API range | message | `invalid --cpu` |
| `--memory` | 6 | API range | message | `invalid --memory` |
| `--storage` | 6 | API range | message | `invalid --storage` |
| `--country` | 6 | API country codes | message | `invalid --country` |
| `--port` | 6 | API range | message | `invalid --port` |
| `--queue-port` | 6 | API range | message | `invalid --queue-port` |
| `--replicas` | 6 | API range | message | `invalid --replicas` |
| `--priority` (create and update) | 6 | API enum | message | `invalid --priority` |
| `--restart-policy` | 6 | API enum | message | `invalid --restart-policy` |
| `--deletion-cost` | 6 | API range | message | `invalid --deletion-cost` |
| negative `--summarize` | 6 | the endpoint's range (0-2000) | message | `invalid --summarize` |
| `--severity` | 6 | API enum | message | `invalid severity` |
| a GPU class name (`--gpu`, `gpus get`) that resolves to nothing | 6 | the org's GPU classes | message | `unknown GPU class` |
| a GPU class name (`--gpu`, `gpus get`) that resolves to several | 6 | the org's GPU classes | message | `ambiguous GPU class` |
| `--gpu` name with no class list to resolve it | 6 | the org's GPU classes | message | `name resolution is unavailable` |
| `ai-gateway test --model` | 6 | the key's models | message | `is not available to this key` |
| instance ID prefix matching several | 6 | the group's instances | message | `is ambiguous in container group` |
| a job ID that is not a full UUID | 6 | the API's job ID format | message | `job IDs must be full UUIDs` |
| `--input` that is not JSON | 6 | the job document | message | `invalid --input:` |
| `--input-extra` that is not a JSON object | 6 | the job document | message | `invalid --input-extra` |
| a job prototype without `input` | 6 | the job document | message | `invalid job prototype` |
| a definition missing a required field | 6 | the API schema | message | `invalid container group definition` |
| a patch file that is not an object | 6 | the API schema | message | `patch file is not a JSON object` |
| an empty patch (`null`, nothing, or `{}`) | 6 | the API schema | message | `empty patch document` |
| a field the CLI's API model does not know | 6 | the API schema | message | `unknown field` |
| a `get` response given as a patch | 6 | the API schema | message | `response, not a patch` |
| a definition and flags that cannot be merged | 6 | the API schema | message | `cannot merge documents` |
| a container command over 100 words | 6 | API range | message | `the API allows at most` |
| an empty container command word | 6 | the node drops empty words | message | `is empty; the platform drops empty command words` |
| a container command starting with `-` | 6 | the node runs the first word as the program | hint | `start it with a program` |
| an `update` container change the platform rejected or left as it was, once the change settles | 6 | the platform's check of the new version | message | `did not apply the container change` |
| a file the user named cannot be read | 6 | the user's file | message | `cannot read` |
| a file the user named cannot be parsed | 6 | the user's file | message | `cannot parse` |
| an audio file that does not exist | 6 | the user's file | hint | `pass a local file path or an http(s) URL` |
| an audio path that is a directory | 6 | the user's file | message | `is a directory` |
| `agent init --dir` that is not a directory | 6 | the user's path | message | `is not a directory` |
| a malformed block in the file `agent init` writes | 6 | the user's file | message | `malformed SaladCloud block` |
| `login` without a terminal and with no key from `--api-key`, stdin or `SALAD_API_KEY` | 3 | no usable credential | hint | `run saladcloud login in a terminal` |
| a command that needs the API key with none set | 3 | no usable credential | message | `no API key set` |
| a key with a character no key has (outside printable ASCII) | 3 | no usable credential | message | `contains characters an` |
| `login` with no new key while one is saved | 2 | structure | message | `and a key is already saved` |
| `login --api-key -` with nothing on stdin | 2 | missing value | message | `got no key on stdin` |
| an empty `--api-key`, `--org` or `--project` on `login` | 2 | missing value | message | `%s is empty` |
| `login --org` with a `/` | 2 | structure | message | `takes an organization only` |
| `ai-gateway login` without a key | 3 | no usable credential | message | `no AI Gateway key` |

## Settled decisions

Each of these was argued out; reopen one only with a reason the original did not have.

- **Container groups have their own noun**, `container-groups` (aliases `container-group`, `cg`), instead of owning bare
  verbs, so a future compute primitive gets the same shape without colliding with `create`, `list`, or `ssh`. Plural
  like the other resource nouns (`orgs`, `gpus`, `quotas`, `instances`, `queues`); product areas (`inference`,
  `ai-gateway`, `config`) are singular. `logs` is top level because log queries are organization-scoped and a
  container group is only a filter.
- **Leaf positional, ancestors as flags.** The thing a verb acts on is its one positional argument; every ancestor above
  it is a named flag (`--org`, `--project`, `-c/--container-group`, `--endpoint`, `--queue`), as fly, kubectl and
  gcloud do; none chains two positionals. The one exception is the instance on `ssh`, `sftp` and
  `container-groups logs`: the group is their subject, so it is the positional and the instance is `--instance` (the
  first running instance when omitted). A *reference to another resource* given as one value may be
  `scope/name` (`cp`'s `group[/instance]:path`, `create --from [project/]group`, `availability --from`); a command's own
  target never is. `use <org>[/<project>]` sets the context pair the other commands read and is exempt.
- **Resource nouns take a verb.** A bare resource noun is a usage error (exit 2, the error document on stderr), never
  help on stdout with exit 0, which would make `gpus -o json | jq` fail silently; help is `--help`. Query commands
  (`logs`, `availability`) act directly and take their filters as flags; a future need such as watching is a flag, not a
  verb.
- **Alias rule.** Every plural resource noun answers to its singular; a noun may also answer to its full name and its
  API resource name. `cg` is the one alias that abbreviates; `orgs` and `gpus` are primary names whose full forms are
  aliases. No synonyms. The first guess at a noun, singular or plural, always resolves, while a closed set keeps help
  and completion free of synonyms that would have to be supported forever. `commandtree_test.go` derives the singular
  rule and holds the table of the rest.
- **Singletons take `get`, never `list`.** A resource with one instance per organization or account (`quotas`,
  `webhook-secret`, `ssh-key`) takes `get` plus action verbs (`rotate`, `setup`), keeps the API's spelling
  (so `quotas` stays plural), and has no singular alias. `config` is not a singleton but a key/value store with gh's
  verbs (`get <key>`, `set`, `unset`, `list`, `path`). `list` would promise several and hand scripts a one-item array to
  unwrap; the API's spelling keeps the CLI and the API reference searchable together.
- **`inference` is both the product area and the endpoints resource**, the one singular resource noun that takes
  `list`/`get` (`config` is a key/value store, not a resource). Its porcelain is one command per endpoint, named exactly
  after the endpoint, verb or not (`transcribe`, `transcription-lite`), whose flags are that endpoint's input fields
  kebab-cased; a flag not set is not sent. `list`, `get`, `jobs`, `help` and `completion` are reserved; an endpoint with
  a reserved name, or one newer than the CLI, is reached through `inference jobs create --endpoint`. Porcelain never
  moves to the top level. The API nests endpoints directly under inference, and the endpoint's name is the one the
  portal and the docs use, so porcelain named after it needs no lookup table; the reserved names keep porcelain from
  shadowing the resource verbs.
- **Short flags are a closed table**: `-o` output, `-f` file (write verbs) / follow (`logs`), `-e` env, `-c` container
  group, `-w` watch, `-i` identity (`ssh`, `cp`, `sftp`), `-r` recursive (`cp`). Nothing else gets a short (`-g` reads
  as GPU, `-e` is env everywhere, `-q` reads as quiet); completion makes the long forms cheap.
- **Every list emits `{"items": [...]}`; repeatable flags are repeated, never comma-split** (GPU class names contain
  commas). One envelope is one jq path (`.items[]`) for every list, with room for paging fields beside it later.
- **Environment variables: `SALAD_` for the four shared names, `SALAD_CLI_` for the rest.** `SALAD_API_KEY`,
  `SALAD_ORGANIZATION`, `SALAD_PROJECT` and `SALAD_AI_GATEWAY_API_KEY` are spelled as the platform's docs and SDKs spell
  them; every variable only this CLI reads is `SALAD_CLI_`-prefixed; no other prefix, no fallback to old names. A shell
  set up for the docs' examples works unchanged, and a CLI-only knob cannot collide with a name the platform defines.
  Variables the OS or a tool sets (`HOME`, `MSYSTEM`, `CI`) are read as facts about where the CLI runs, never named
  by it. `internal/config/envrule_test.go` checks every tracked file.
- **One spelling per concept.** Each concept has one English name, spelled on each surface by that surface's case rule:
  kebab-case for flags, config keys and table rows, snake_case for JSON keys, `SALAD_` upper snake for environment
  variables. Learn a name once and every other spelling follows; synonyms across surfaces were the commonest beta
  confusion. This table is the source of truth:

  | Concept | Flag | Env | Config key | JSON key | Table row |
  | --- | --- | --- | --- | --- | --- |
  | API key | `--api-key` (login) | `SALAD_API_KEY` | `api-key` | `api_key` | `api-key` |
  | Organization | `--org` | `SALAD_ORGANIZATION` | `organization` | `organization` | `organization` |
  | Project | `--project` | `SALAD_PROJECT` | `project` | `project` | `project` |
  | AI Gateway key | `--api-key` (ai-gateway login) | `SALAD_AI_GATEWAY_API_KEY` | `ai-gateway-api-key` | `ai_gateway_api_key` | `ai-gateway-api-key` |

  `--org` is the one short spelling, as fly and gh spell it.
- **`-f` is file on write verbs and follow on `logs`**, as in docker and kubectl; the two never meet on one command.
- **No command calls availability implicitly.** Availability queries are expensive on the platform, so `gpus` is the
  catalog and prices, and `availability` is the one command that asks, once per GPU class named, plus one combined query
  for two or more classes. It never shows a computed total: the combined query is its own query, taken separately, so
  it neither equals nor bounds the rows' sum (a node carrying two classes counts once in it, and counts move between
  queries). A query whose counts all come back zero is asked once more (the service
  answers all zeros between good answers), so the worst case is twice the queries named.
- **The tree-wide did-you-mean is permanent.** An unknown command name lists up to four commands of that name
  elsewhere in the tree (a noun with the verb typed after it, or a command whose `SuggestFor` lists the word, such as
  `status` for `whoami`: a suggestion, never an alias), or the nearest spellings under the same parent for a typo, on
  one line; it is the generic answer to a command spelled in the wrong place, not a shim for renamed ones.
  Grammar changes are hard switches: no aliases or transition messages for the old spelling. The same goes for a
  command line in the wrong shape: a usage error prints the command to type (`usage.go`), never cobra's or pflag's
  wording, and a misplaced argument is never accepted in its wrong place.
- **`delete` is unconfirmed** and there is no dry run: the CLI is non-interactive-first.
- **No `--yes`.** Nothing prompts outside `login`, `ai-gateway login` and `ssh-key setup`, which each have a
  non-interactive form, so a flag that skips a prompt would be a no-op that scripts copy around and agents add "to be
  safe".
- **`update` and `scale` settle by default.** The intent diff can only run against settled state, and whether a patch
  stages depends on its contents (an image change does, an env change does not). An opt-in wait made the warnings appear
  for some patches and not others, which teaches people to ignore them.
- **No `edit` verb; `get --writable` instead.** An editor is the one verb with no scripted use; agents need a patchable
  document, which `--writable` gives without an editor or TTY.
- **No standalone `wait` verb.** `--wait` on create/start/stop, the settle-by-default writes, and `update --wait-ready`
  (the third shape: the replicas on the new version and ready) cover every poll shape so far; each is a flag on the
  write it follows. A new shape gets a flag too, not a verb.
- **The live suite is not run in CI.** It needs an API key and spends money, so `just test-live` is a manual step before
  each release.
- **The CLI ships its own HTTP transport.** The SDK's transport validation rejects real responses (7-digit fractional
  timestamps, JSON nulls such as `current_state.description`); the SDK is used for types only. Regression tests are in
  `internal/api/rest_test.go`.
- **No AI Gateway chat client.** The gateway is OpenAI-compatible so that every client library already speaks it;
  `ai-gateway test` is a key check that happens to print a reply.
- **Output rule.** A verb whose API call returns the resource emits it (`create`, `update`, `get`, `list`,
  `instances update`); a verb whose API call returns no body emits the action shape (`start`, `stop`, `delete`,
  `restart`, `recreate`, `reallocate`, `queues delete`, `jobs cancel`, `logout`); porcelain emits what you asked for and
  whether it happened (`scale` with `{name, action, replicas, ok}`, `transcribe` with the transcript). `--keep-schedule`
  stays on `scale` only. A script gets the API's own resource whenever there is one, never a CLI paraphrase of it, and a
  bodiless call still leaves something stable to check.
- **`--watch` is table mode only.** It re-renders a table for a person; a stream of JSON documents is not one document,
  so `-w` with `-o json` is a usage error that points at `--wait` or a shell loop. Off a terminal it never writes
  control codes.
- **Error kinds are a closed set.** Scripts switch on `kind`, so it is a contract like the exit codes: a `clierr.Kind`
  constant, never a literal or a kind derived from a status string, and a new failure mode maps onto an existing kind.
  `internal/clierr/kinds_test.go` walks every call site and checks the guide's table. `interrupted` is Ctrl-C during a
  wait, a prompt or a request; a canceled request reads `<command> canceled` (`Run` names it), while
  `logs --follow` and `--watch` end with exit 0.

## How to add or change a verb

1. New file in `internal/cli/` with an `(app *App) addXxxCommands(parent)` method that `New` calls. The command's flags
   bind to the fields of an options struct the command owns (never read back by name), and code that needs the run
   is an `*App` method. `RunE` must return a `*clierr.Error` (wrap anything else with `wrapErr`) so `Run` maps it to
   the right exit code instead of reporting a usage error.
2. Render through `app.printer.Emit(v, tableFn)` (or `Stream` for line-oriented output). Define a result struct with
   json tags for every key, no `omitempty`; pointer types for values that can be unset. A verb that emits an SDK type
   directly gets the key fill from `output` for free.
3. Tests in the same package: unit tests for the pure logic, plus a JSON-shape test asserting every key is present and
   null when unset (see `configcmd_test.go`, `agent_test.go`); a newly emitted SDK type gets a case in
   `TestSDKBackedJSONShape` (`shape_test.go`). Run a command line through `newTestRun(t)` and its `runRoot` (a fresh
   tree on fake deps whose environment is a map the test sets with `setenv`), or a harness `session` for the whole run
   against the fake API (`harness_test.go`); every test and subtest calls `t.Parallel()`, and none sets the process
   environment. Declare the new command's output kind in `outputkind_test.go`, add its result to `keysnapshot_test.go`
   and regenerate the golden file (`go test ./internal/cli -run TestJSONKeySnapshot -update-keys`), and give it a row in
   docs/commands.md (`docscommands_test.go` checks both directions and the alias table). A verb that prints to stderr,
   prompts, or writes the config file also gets a case in `invariants_test.go`'s lists (the error document and json-mode
   stderr, no credential in any output, the override warning), which nothing adds for you. Status lines go through
   `say`/`hintLine` and prompts through `ask`/`tell` (a direct write to `app.errw()` must be table-mode only), and
   whether a person can be asked is `atTerminal()`, which counts `CI` and `SALAD_CLI_NO_PROMPT`; `term.IsTerminal` is
   for what a stream is (watch's stdout), so json mode and CI stay quiet.
4. If it calls the API, add a case to `internal/livetest` and run it before releasing.
5. Update `docs/commands.md` (plus a section in `docs/guide.md` if the verb has semantics), the CHANGELOG, and
   `internal/agentdoc` only if the CLI's public contract for agents changed. CHANGELOG entries go under
   `## [Unreleased]` and name what changed for users, not how it is documented.

## Working on the platform, not the CLI

CLI bugs and API behavior that contradicts the API reference both go to GitHub issues on this repository: the request,
the response, and the behavior you expected, never a key. Maintainers pass API problems on to the platform team. The
CLI works around an API behavior only once `docs/notes.md` documents what the user sees; it never works around one
silently. Platform asks, evidence and internal locations stay out of this repository.
