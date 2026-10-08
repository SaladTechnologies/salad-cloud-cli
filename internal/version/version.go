// Package version holds build-time version metadata, injected via -ldflags,
// and the release-stage facts every surface that names the version repeats.
package version

import "runtime/debug"

// Version is the CLI version, set at build time via
// -ldflags "-X github.com/saladtechnologies/salad-cloud-cli/internal/version.Version=...".
// A `go install …@<version>` build carries no ldflags; init falls back to
// the module version Go records in the binary.
var Version = "dev"

// Commit is the git commit, set at build time via
// -ldflags "-X github.com/saladtechnologies/salad-cloud-cli/internal/version.Commit=...".
var Commit = "none"

// Stage is the release stage, shown by --help, version, and the block
// agent init writes. Drop it when the CLI leaves beta.
const Stage = "beta"

// IssuesURL is where bugs and unexpected behavior are reported.
const IssuesURL = "https://github.com/SaladTechnologies/salad-cloud-cli/issues"

func init() {
	if Version != "dev" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		Version = moduleVersion(bi.Main.Version, Version)
	}
}

// moduleVersion returns the module version from build info, or fallback
// when there is none: "(devel)" is what a local `go build` records.
func moduleVersion(main, fallback string) string {
	if main == "" || main == "(devel)" {
		return fallback
	}
	return main
}
