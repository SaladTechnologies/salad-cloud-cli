# Changelog

All notable changes to the SaladCloud CLI. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow semver. During the beta nothing is promised stable: commands, flags, exit codes and JSON output shapes
may change, and every such change is listed here.

## [Unreleased]

### Added

- `container-groups update --wait-ready` waits, after the change settles, until every replica runs the group's new
  version, is running and is ready; `--ready-for 5m` also requires them to stay ready that long. `--timeout` then caps
  the whole wait and defaults to `1h`. A rollout that is still pulling at the deadline exits 1 (`wait_timeout`) with a
  message that says the update was applied and where the replicas were.
- `container-groups update` says on stderr when the change made a new version of a group with replicas
  (`note: my-app is now version 3; its instances move onto it as their nodes check in, ...`): the settled document still
  shows the instances that run the old one.

### Changed

- Root `--help` says which arguments are positional and which are flags, and points coding agents at
  `saladcloud agent init --print`. The block `agent init` writes now names the exception: `cg ssh`, `cg sftp` and
  `cg logs` take the group as their argument and the instance as `--instance`.

### Fixed

- `container-groups get --writable` keeps the empty lists a group has, such as an HTTP probe's `headers`, so `update -f`
  takes its document back. Before, the API rejected a group with an HTTP probe with `Headers field is required`.
- `container-groups list --projects` reports an organization the API key cannot see as `organization "<org>" not found`
  (exit 4). Before, it skipped every named project with `warning: project "<project>" not found, skipped` and exited 0.
  A project that is missing from an organization the key can see is still skipped, and the warning now names the
  organization. `--projects`, `create --from` and `availability --from` also report a rejected API key the way every
  other command does.
- In Git Bash, `container-groups ssh <group> -- <command>` stops with a usage error (exit 2) when a word of the command
  was rewritten into a Windows path (`-- ls /opt` arrives as `ls C:/Program Files/Git/opt`), as `create` already does
  for a container command. Before, it ran the command on the rewritten path.

### Security

- Release binaries and the container image are built with Go 1.27.4, which fixes a crash in Go's HTTP/1 server
  ([GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859)). The CLI runs no HTTP server, so it was not exposed, but
  vulnerability scanners flag binaries built with an earlier Go. A `go install` build uses your own Go, so keep it up to
  date to get the same fix.
- A redirect to another host no longer carries the API key, the AI Gateway key or the request's body.
- `SALAD_CLI_API_BASE_URL`, `SALAD_CLI_STORAGE_BASE_URL` and `SALAD_CLI_AI_GATEWAY_BASE_URL` must be https unless they
  name localhost (exit 2 otherwise), and one that names another machine prints a `warning:` on stderr.
- `--user` on `container-groups ssh`, `sftp` and `cp` must be a user name, and `cp` refuses a path with a control
  character (both exit 2).
- `container-groups ssh`, `sftp` and `cp` refuse an SSH endpoint from the API that is not an address and a port (exit
  1), and `ssh --config` leaves such an instance out with a `warning:`.
- `container-groups ssh --config` quotes an identity file path that holds a space, and refuses one with a control
  character or a double quote (exit 2).

## [0.1.0] - 2026-11-09

The first public beta of the SaladCloud CLI: a thin, non-interactive-first command-line wrapper over the SaladCloud
Public API for people, scripts, CI and AI agents. **Expect bugs and changes**, and please report anything that looks
wrong at <https://github.com/SaladTechnologies/salad-cloud-cli/issues>.

### Security

- Release binaries and the container image are built with Go 1.27.3, whose certificate verification distrusts
  compromised intermediate certificates when the CLI connects over HTTPS
  ([GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809)). A `go install` build needs Go 1.26 or later and uses your own
  Go, so keep it up to date to get the same fix.
