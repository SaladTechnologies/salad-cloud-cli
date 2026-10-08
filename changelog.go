// Package saladcloudcli is the root of the SaladCloud CLI's module. The CLI is
// the command in [github.com/saladtechnologies/salad-cloud-cli/cmd/saladcloud];
// install a prebuilt release (see the README), or build it with Go:
//
//	go install github.com/saladtechnologies/salad-cloud-cli/cmd/saladcloud@latest
//
// This package only embeds files that live at the repository root, which a go:embed
// directive in a subpackage cannot reach. It is not meant to be imported.
package saladcloudcli

import _ "embed"

// Changelog is the contents of CHANGELOG.md at build time, served by
// `saladcloud changelog` so an agent or script can read what changed
// without network access or a checkout.
//
//go:embed CHANGELOG.md
var Changelog string
