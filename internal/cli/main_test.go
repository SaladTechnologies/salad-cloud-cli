package cli

import (
	"bytes"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
)

// TestMain runs the test binary as a child process when a test starts it as
// one (fakeChild), and the tests otherwise.
func TestMain(m *testing.M) {
	if code, ok := fakeChild(); ok {
		os.Exit(code) //nolint:forbidigo // the test binary run as sftp or ssh exits as they would
	}
	m.Run()
}

// fakeChild is the test binary standing in for a child process, by the
// variable its parent test set (testDeps' Environ passes the run's
// variables on): SALAD_CLI_TEST_FAKE_SFTP prints sftp's batch echo and
// exits, failing when it is "fail"; SALAD_CLI_TEST_HELPER_EXIT exits 255, as
// ssh does when it cannot open the session.
func fakeChild() (int, bool) {
	if os.Getenv("SALAD_CLI_TEST_HELPER_EXIT") != "" {
		return 255, true
	}
	switch os.Getenv("SALAD_CLI_TEST_FAKE_SFTP") {
	case "":
		return 0, false
	case "fail":
		fprintln(os.Stdout, `sftp> get "/etc/hostname" "./h"`)
		fprintln(os.Stderr, `remote open("/etc/hostname"): Permission denied`)
		return 1, true
	default:
		fprintln(os.Stdout, `sftp> get "/etc/hostname" "./h"`)
		return 0, true
	}
}

// testDeps are a test's dependencies: the environment is env and nothing
// from the process, output goes nowhere, stdin is empty, there is no
// terminal, no page opens, and the CLI's known_hosts is /cfg/known_hosts (the
// relay argv tests compare exact argument lists).
func testDeps(env map[string]string) Deps {
	d := ProcessDeps()
	d.Args = nil
	d.Stdin = strings.NewReader("")
	d.Stdout, d.Stderr = io.Discard, io.Discard
	d.Env = config.Env{Getenv: func(k string) string { return env[k] }, GOOS: runtime.GOOS}
	d.Console = &fakeConsole{terminal: false, answers: nil, device: bytes.Buffer{}, transcript: nil}
	d.OpenPage = func(string) error { return nil }
	d.KnownHostsPath = func() string { return "/cfg/known_hosts" }
	d.Environ = func() []string {
		environ := os.Environ()
		for k, v := range env {
			environ = append(environ, k+"="+v) // a later entry wins
		}
		return environ
	}
	return d
}

// testRun is one test's run of the CLI: an App on testDeps, whose
// environment the test changes with setenv, and the command tree built for
// it. Each command line a test runs (runRoot, runExecute, runComplete) gets
// a fresh App and tree from the run's deps, as a process would.
type testRun struct {
	*App

	env  map[string]string
	root *cobra.Command
}

// newTestRun is a test's run with a temp config dir and home and no other
// variable set.
func newTestRun(t *testing.T) *testRun {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	env := map[string]string{"SALAD_CLI_CONFIG_DIR": filepath.Join(dir, "config"), "HOME": home, "USERPROFILE": home}
	r := &testRun{App: newApp(testDeps(env)), env: env, root: nil}
	r.root = New(r.App)
	return r
}

// clone is a run with app's environment, seams and output mode that the test
// changes without changing app: a parallel subtest's own copy of its parent's
// setup.
func (app *testRun) clone(t *testing.T) *testRun {
	t.Helper()
	env := maps.Clone(app.env)
	d := app.Deps
	own := testDeps(env)
	d.Env.Getenv, d.Environ = own.Env.Getenv, own.Environ
	c := &testRun{App: newApp(d), env: env, root: nil}
	if cerr := c.printer.SetMode(app.printer.Mode()); cerr != nil {
		t.Fatal(cerr)
	}
	c.root = New(c.App)
	return c
}

// setenv sets the variable k to v for the run's later reads.
func (app *testRun) setenv(k, v string) { app.env[k] = v }

// fresh is a new App and tree on the run's deps as they are now: its
// environment, seams and streams.
func (app *testRun) fresh() (*App, *cobra.Command) {
	a := newApp(app.Deps)
	return a, New(a)
}

// runRoot runs one command line in-process on a fresh tree and returns its
// stdout and the error Execute returned.
func (app *testRun) runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	a, root := app.fresh()
	a.printer = output.NewPrinter(&buf)
	root.SetArgs(args)
	err := root.ExecuteContext(t.Context())
	return buf.String(), err
}

// findCommand returns the command at path, failing the test when the tree
// has none.
func (app *testRun) findCommand(t *testing.T, path ...string) *cobra.Command {
	t.Helper()
	c, _, err := app.root.Find(path)
	if err != nil {
		t.Fatalf("%v: %v", path, err)
	}
	return c
}

// setOutputMode selects the run's output mode, reporting a mode SetMode
// rejects.
func (app *testRun) setOutputMode(t *testing.T, mode string) {
	t.Helper()
	if cerr := app.printer.SetMode(mode); cerr != nil {
		t.Error(cerr)
	}
}
