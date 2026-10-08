// Package saladcloudcli is the module root. It exists to embed files that
// live at the repository root, which go:embed cannot reach from a subpackage.
package saladcloudcli

import _ "embed"

// Changelog is the contents of CHANGELOG.md at build time, served by
// `saladcloud changelog` so an agent or script can read what changed
// without network access or a checkout.
//
//go:embed CHANGELOG.md
var Changelog string
