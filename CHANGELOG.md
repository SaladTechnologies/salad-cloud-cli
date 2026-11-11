# Changelog

All notable changes to the SaladCloud CLI. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow semver. During the beta nothing is promised stable: commands, flags, exit codes and JSON output shapes
may change, and every such change is listed here.

## [Unreleased]

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

### Security

- Release binaries and the container image are built with Go 1.27.4, which fixes a crash in Go's HTTP/1 server
  ([GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859)). The CLI runs no HTTP server, so it was not exposed, but
  vulnerability scanners flag binaries built with an earlier Go. A `go install` build uses your own Go, so keep it up to
  date to get the same fix.

## [0.1.0] - 2026-11-09

The first public beta of the SaladCloud CLI: a thin, non-interactive-first command-line wrapper over the SaladCloud
Public API for people, scripts, CI and AI agents. **Expect bugs and changes**, and please report anything that looks
wrong at <https://github.com/SaladTechnologies/salad-cloud-cli/issues>.

### Security

- Release binaries and the container image are built with Go 1.27.3, whose certificate verification distrusts
  compromised intermediate certificates when the CLI connects over HTTPS
  ([GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809)). A `go install` build needs Go 1.26 or later and uses your own
  Go, so keep it up to date to get the same fix.
