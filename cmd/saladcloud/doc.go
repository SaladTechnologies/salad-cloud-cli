// Saladcloud is the SaladCloud CLI: a thin, non-interactive-first command-line
// client for the SaladCloud Public API, built for people, scripts, CI and AI
// agents. It deploys and manages container groups, queries their logs, runs
// jobs on queues and inference endpoints, and lists GPU classes with their
// prices and availability.
//
// Install a prebuilt release (see the README), or build it with Go 1.26 or
// later:
//
//	go install github.com/saladtechnologies/salad-cloud-cli/cmd/saladcloud@latest
//
// Then log in and look around:
//
//	saladcloud login
//	saladcloud container-groups list
//	saladcloud --help
//
// Every command that reports a result prints one JSON document with -o json,
// exit codes are a closed set, and in JSON mode an error is a single JSON line
// on stderr; the guide documents that contract. The CLI is in beta: commands,
// flags and output shapes may change, and the changelog lists every change
// (saladcloud changelog prints it offline).
//
// # Documentation
//
//   - [README]: install, uninstall and getting started
//   - [Commands]: every command, its flags and aliases
//   - [Guide]: output, exit codes, error kinds, and how each area works
//   - [Notes]: CLI and platform behavior worth knowing
//
// [README]: https://github.com/SaladTechnologies/salad-cloud-cli#readme
// [Commands]: https://github.com/SaladTechnologies/salad-cloud-cli/blob/main/docs/commands.md
// [Guide]: https://github.com/SaladTechnologies/salad-cloud-cli/blob/main/docs/guide.md
// [Notes]: https://github.com/SaladTechnologies/salad-cloud-cli/blob/main/docs/notes.md
package main
