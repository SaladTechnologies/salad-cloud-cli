# Changelog

All notable changes to the SaladCloud CLI. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow semver. During the beta nothing is promised stable: commands, flags, exit codes and JSON output shapes
may change, and every such change is listed here.

## [Unreleased]

### Changed

- Building from source with `go install` now needs Go 1.25 or later (was 1.24).
- The CLI spells "canceled" the US way everywhere it owns the word. **Breaking for scripts:** the error kind
  `job_cancelled` is now `job_canceled`, and `inference jobs cancel` and `queues jobs cancel` emit
  `"action": "canceled"`. Messages change too (`login canceled, nothing changed`, `<command> canceled` after Ctrl-C). A
  job's `status` and event `action` still read `cancelled`, as the API sends them.

## [0.1.0-beta.1] - 2026-10-08

The first public beta of the SaladCloud CLI: a thin, non-interactive-first command-line wrapper over the SaladCloud
Public API for people, scripts, CI and AI agents. **Expect bugs and changes**, and please report anything that looks
wrong at <https://github.com/SaladTechnologies/salad-cloud-cli/issues>.
