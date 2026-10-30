# Changelog

All notable changes to the SaladCloud CLI. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow semver. During the beta nothing is promised stable: commands, flags, exit codes and JSON output shapes
may change, and every such change is listed here.

## [Unreleased]

### Fixed

- `container-groups update` now fails when the platform rejects the container change it asked for, such as an image tag
  that does not exist or a private image it cannot pull. It used to exit 0 with the group still on its old image and
  only a `warning:` line on stderr. It now exits 6 (kind `invalid_input`) with empty stdout and the platform's reason in
  the message (`the API did not apply the container change to my-app: Version 2: Manifest Not Found`). When the patch
  changed the image without `container.registry_authentication`, the hint says that a private image needs its registry
  credentials again in the `-f` file, whatever registry it is on, Docker Hub included.

### Security

- Release binaries and the container image are built with Go 1.27.3, whose certificate verification distrusts
  compromised intermediate certificates when the CLI connects over HTTPS
  ([GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809)). A `go install` build uses your own Go, so update it to get the
  same fix.

## [0.1.0-beta.1] - 2026-10-21

The first public beta of the SaladCloud CLI: a thin, non-interactive-first command-line wrapper over the SaladCloud
Public API for people, scripts, CI and AI agents. **Expect bugs and changes**, and please report anything that looks
wrong at <https://github.com/SaladTechnologies/salad-cloud-cli/issues>.
