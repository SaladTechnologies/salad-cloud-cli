# Changelog

All notable changes to the SaladCloud CLI. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow semver. During the beta nothing is promised stable: commands, flags, exit codes and JSON output shapes
may change, and every such change is listed here.

## [Unreleased]

### Security

- Release binaries and the container image are built with Go 1.27.3, whose certificate verification distrusts
  compromised intermediate certificates when the CLI connects over HTTPS
  ([GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809)). A `go install` build uses your own Go, so update it to get the
  same fix.

## [0.1.0-beta.1] - 2026-10-21

The first public beta of the SaladCloud CLI: a thin, non-interactive-first command-line wrapper over the SaladCloud
Public API for people, scripts, CI and AI agents. **Expect bugs and changes**, and please report anything that looks
wrong at <https://github.com/SaladTechnologies/salad-cloud-cli/issues>.
