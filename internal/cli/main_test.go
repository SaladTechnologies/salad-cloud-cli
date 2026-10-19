package cli

import (
	"testing"
)

// TestMain prepares the command tree once before any test, as Execute does
// before any run. Reading a command's local or inherited flags makes cobra
// copy the global --org and --project into it, so a test that did so first
// would leave contextFree's and orgScoped's hidden shadows too late for every
// test after it.
func TestMain(m *testing.M) {
	app = newApp(ProcessDeps())
	// The relay argv tests compare exact argument lists, so the CLI's
	// known_hosts path is fixed for the whole package.
	app.KnownHostsPath = func() string { return "/cfg/known_hosts" }
	prepareTree()
	m.Run()
}
