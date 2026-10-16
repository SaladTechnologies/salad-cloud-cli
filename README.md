# SaladCloud CLI

> [!WARNING] **This is a beta release.** Expect bugs and changes: commands, flags, messages, exit codes and JSON output
> may all still change, and some behavior will surprise you. Every change is listed in the [changelog](CHANGELOG.md),
> and the [exit codes and JSON output](docs/guide.md#exit-codes-and-json-output) are the parts we work hardest to keep
> stable. **Please [report anything that looks wrong](#reporting-issues)**; during the beta, those reports are the most
> useful thing you can send us.

A thin command-line wrapper over the [SaladCloud Public API](https://docs.salad.com/reference/api-reference), built for
people, scripts, CI, and AI agents. Every command works non-interactively and exits with a documented code, and every
command that reports a result supports `--output json`. The command is `saladcloud`.

## Install

**macOS and Linux:**

```sh
curl -fsSL https://raw.githubusercontent.com/SaladTechnologies/salad-cloud-cli/main/install.sh | sh
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/SaladTechnologies/salad-cloud-cli/main/install.ps1 | iex
```

Both scripts download the newest release for your machine, verify it against the release's `checksums.txt`, and install
`saladcloud`: to `~/.local/bin` on macOS and Linux (they tell you if that is not on your `PATH`), and to
`%LOCALAPPDATA%\Programs\saladcloud` on Windows, which is added to your user `PATH`. Both read the same variables:
`SALAD_CLI_VERSION` (for example `v0.1.0-beta.1`) pins a release, `SALAD_CLI_INSTALL_DIR` chooses the folder,
`SALAD_CLI_REPO` and `SALAD_CLI_DOWNLOAD_URL` point at another repository or a mirror of its release files,
`SALAD_CLI_NO_MODIFY_PATH=1` leaves your Windows user `PATH` alone, and `GITHUB_TOKEN` is sent when looking up the
latest release, to avoid GitHub's rate limit. Prebuilt binaries cover Linux amd64 and arm64, macOS arm64 and amd64, and
Windows amd64 (which also runs on Windows 11 on ARM under emulation).

Other ways to install:

- **Manual download:** the archives and `checksums.txt` are on each
  [release](https://github.com/SaladTechnologies/salad-cloud-cli/releases). Every archive carries a build provenance
  attestation: `gh attestation verify <archive> --repo SaladTechnologies/salad-cloud-cli`.
- **With Go 1.25 or later**, on any platform Go supports:
  `go install github.com/saladtechnologies/salad-cloud-cli/cmd/saladcloud@latest`
- **From source:** `make build` (needs bash and Go) writes `dist/<os>-<arch>/saladcloud`.

Run `saladcloud version` to confirm the install; it prints the version and commit you will want in any bug report.

Next, log in:

```sh
saladcloud login
```

Shell completion knows your container group, instance, queue, endpoint and GPU class names. Load it from your shell's
startup file:

```sh
source <(saladcloud completion bash)          # ~/.bashrc
source <(saladcloud completion zsh)           # ~/.zshrc
saladcloud completion fish | source           # ~/.config/fish/config.fish
saladcloud completion powershell | Out-String | Invoke-Expression   # $PROFILE
```

### Uninstall

**macOS and Linux:**

```sh
curl -fsSL https://raw.githubusercontent.com/SaladTechnologies/salad-cloud-cli/main/uninstall.sh | sh
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/SaladTechnologies/salad-cloud-cli/main/uninstall.ps1 | iex
```

These undo the install scripts: they remove `saladcloud` from the folder the installer used (`SALAD_CLI_INSTALL_DIR`, if
you set it for the install), and on Windows that folder from your user `PATH`. Your config file, which holds your saved
API keys, stays so a reinstall picks up where you left off; set `SALAD_CLI_PURGE=1` to remove it too
(`saladcloud config path` shows where it is). Remove any `saladcloud completion` line you added to a startup file. For
`go install` or a manual download, delete the binary.

## Getting started

### Before you start, in the portal

A few steps are done in the [SaladCloud Portal](https://portal.salad.com):

1. **Create an account** at [portal.salad.com](https://portal.salad.com).
2. **Create an organization.** It comes with a project named `default`; add more projects in the portal if you want to
   keep environments apart.
3. **Add a billing method and credits** on the organization's **Billing & Usage** page. Without credits, you cannot
   create or start container groups, or use any other SaladCloud service.

`saladcloud login` then asks for your API key, from the portal's API key page (press Enter at the prompt to open it),
and for your organization and project. The key is your user's, and works for every organization you are a member of.

Two features need one more portal step:

- **`container-groups ssh`, `cp`, and `sftp`** need an SSH public key registered to your account, at
  <https://portal.salad.com/ssh-keys>: `saladcloud ssh-key setup` finds or generates one and prints it.
- **`ai-gateway test`** needs an AI Gateway key, created on the organization's AI Gateway page (linked from
  <https://portal.salad.com/ai-gateway>): run `saladcloud ai-gateway login`.

### First commands

```sh
# Save your API key, organization, and project
saladcloud login

# List GPU classes with prices
saladcloud gpus list

# Deploy a container group: a public busybox image that logs a line every 5 seconds
saladcloud container-groups create my-first-group --image docker.io/library/busybox:1.36 --cpu 1 --memory 1024 --priority batch -- sh -c 'while true; do echo hello from SaladCloud; sleep 5; done'

# Check its status, instance counts and, if it failed, the reason
saladcloud container-groups get my-first-group

# Follow its logs
saladcloud container-groups logs my-first-group -f

# Delete it
saladcloud container-groups delete my-first-group
```

The create line works the same in bash, zsh and PowerShell. A deleted group's name is never released, so give each new
group a new name. From a checkout of this repository, `-f examples/hello-cpu.yaml` is the same definition as a file; the
[guide](docs/guide.md#a-definition-on-stdin) shows a definition read from stdin in each shell.

Container groups have their own command, `saladcloud container-groups`, and `cg` is short for it: `saladcloud cg list`
lists your container groups. Resource nouns always take a verb (`gpus list`, `orgs get <name>`), and the thing a command
acts on is its one positional argument while its parents are flags (`saladcloud cg instances restart <id> -c my-app`).

`login` asks for the key, then the organization and project. Use their **URL names**, the segments in
`https://portal.salad.com/organizations/<org>/projects/<project>`, not the display names (`saladcloud orgs list` shows
both for organizations; a project's URL name is in the portal URL). `saladcloud use <org>/<project>` switches context
later, and `saladcloud whoami` shows what is in effect and where each value came from.

In scripts and CI, skip `login`: set `SALAD_API_KEY`, `SALAD_ORGANIZATION`, and `SALAD_PROJECT` (or pass
`--org`/`--project`), and add `-o json` for machine-readable output. A group can also be created from a JSON or YAML
definition file, with flags on top: `saladcloud container-groups create -f examples/hello-cpu.yaml`.

Every command has `--help`, and `saladcloud doctor` diagnoses setup and connectivity problems. If you work with a coding
agent, run `saladcloud agent init` in your project so the agent knows how to use the CLI.

## Documentation

- [Guide](docs/guide.md): how to use the CLI, from setup to the contracts scripts rely on
  - [Configuration](docs/guide.md#configuration): login, credentials and precedence, the config file
  - [Container groups](docs/guide.md#container-groups): create flags, definition files, updates, scheduled scaling,
    waiting
  - [Logs](docs/guide.md#logs) and [Availability](docs/guide.md#availability)
  - [Inference endpoints & transcription](docs/guide.md#inference-endpoints--transcription)
  - [Job queues](docs/guide.md#job-queues)
  - [AI Gateway](docs/guide.md#ai-gateway)
  - [Coding agents](docs/guide.md#coding-agents): `agent init` and what it writes
  - [Exit codes and JSON output](docs/guide.md#exit-codes-and-json-output): what scripts and agents can rely on
- [Commands](docs/commands.md): every command at a glance
- [Notes](docs/notes.md)
- [Changelog](CHANGELOG.md), also built in: `saladcloud changelog`

## Reporting issues

The CLI is in beta, so please tell us when something goes wrong:
[open an issue](https://github.com/SaladTechnologies/salad-cloud-cli/issues/new/choose). Worth reporting: a crash, a
wrong exit code, a JSON document missing a key, output that contradicts `--help`, or a message that sent you the wrong
way. The bug template asks for:

- the output of `saladcloud version`
- the exact command you ran
- the error, ideally re-run with `-o json` so it is the single-line `{"error":{...}}` document on stderr
- for setup or connectivity problems, the output of `saladcloud doctor`

**Never paste an API key.** The CLI never prints your API keys (`--debug=body` redacts them, and `webhook-secret get`
prints only the webhook secret, by design), but check anything you copy from your shell, environment, or config file
first. A transcription job's document (`inference jobs get -o json`) contains a working link to the file you uploaded,
valid for 24 hours: remove its `input.url` before sharing it.

- **API behavior that contradicts the [API reference](https://docs.salad.com/reference)** is worth an issue here too:
  include the command, the request and response (with `-o json`), and what you expected. We pass API problems on to the
  platform team.
- **Questions** go to [GitHub discussions](https://github.com/SaladTechnologies/salad-cloud-cli/discussions).
- **Account, billing, or workload problems**, as opposed to CLI bugs, go to [`cloud@salad.com`](mailto:cloud@salad.com).
- **Security vulnerabilities** go to a
  [private security advisory](https://github.com/SaladTechnologies/salad-cloud-cli/security/advisories/new) or
  [`dev@salad.com`](mailto:dev@salad.com), never a public issue. See [SECURITY.md](.github/SECURITY.md).

## Contributing

See [CONTRIBUTING.md](.github/CONTRIBUTING.md). The development workflow, layout, and public contracts are in
[AGENTS.md](AGENTS.md); every user-visible change is recorded in [CHANGELOG.md](CHANGELOG.md). This project follows a
[code of conduct](.github/CODE_OF_CONDUCT.md).

## License

[MIT](LICENSE) © 2026 Salad Technologies, Inc.
