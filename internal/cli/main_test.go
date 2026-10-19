package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

// app and rootCmd are the run and tree the package's tests share.
var (
	app     *App
	rootCmd *cobra.Command
)

// TestMain builds the tests' run and its command tree, as Run does for each
// run.
func TestMain(m *testing.M) {
	app = newApp(ProcessDeps())
	// The relay argv tests compare exact argument lists, so the CLI's
	// known_hosts path is fixed for the whole package.
	app.KnownHostsPath = func() string { return "/cfg/known_hosts" }
	rootCmd = New(app)
	m.Run()
}
