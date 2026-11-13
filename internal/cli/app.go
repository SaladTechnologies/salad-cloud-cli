package cli

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
)

// Deps is what a run takes from its process: the command line, the streams,
// the environment and terminal, and the programs and paths tests replace.
type Deps struct {
	Args   []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Env    config.Env
	// Console is the terminal prompts read and write.
	Console console
	// OpenPage opens a URL in the browser.
	OpenPage func(url string) error
	// SFTPProgram is the sftp client cp drives.
	SFTPProgram string
	// KnownHostsPath is the CLI's own known_hosts file, "" when there is none.
	KnownHostsPath func() string
	// GenerateKey makes a key without a passphrase at keyPath (ssh-keygen).
	GenerateKey func(ctx context.Context, keyPath string) error
	// ProductionAPI is the API doctor reports on when no override is set.
	ProductionAPI string
	// LookPath and HasPassphrase are the system checks doctor's SSH row makes.
	LookPath      func(file string) (string, error)
	HasPassphrase func(ctx context.Context, keyPath string) passphraseCheck
	// Environ is the environment ssh, sftp and cp's child process gets.
	Environ func() []string
}

// ProcessDeps is the running process as a run's dependencies.
func ProcessDeps() *Deps {
	env := config.ProcessEnv()
	return &Deps{
		Args:           os.Args[1:],
		Stdin:          os.Stdin,
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
		Env:            env,
		Console:        &stdConsole{device: nil, keys: nil, in: nil},
		OpenPage:       func(url string) error { return openBrowser(env, url) },
		SFTPProgram:    "sftp",
		KnownHostsPath: func() string { return defaultKnownHostsPath(env) },
		GenerateKey:    sshKeygen,
		ProductionAPI:  api.DefaultBaseURL,
		LookPath:       exec.LookPath,
		HasPassphrase:  keyHasPassphrase,
		Environ:        os.Environ,
	}
}

// App is one run: its dependencies and the state the run builds up.
type App struct {
	Deps

	printer       *output.Printer
	flags         runFlags
	debugLevel    api.DebugLevel
	assumedWarned atomic.Bool
	// baseURLWarned is set once the run has warned about base-URL variables that send requests to another machine.
	baseURLWarned atomic.Bool
	// treeErrs are the errors from building the command tree. Marking a flag
	// required or hidden, or giving it completion, fails only for a flag the
	// command does not define or completion given twice: a bug, which Run
	// reports before it runs anything.
	treeErrs []error
}

// runFlags are the flags a run reads whichever command defines them: the
// root's, and the ancestor flags (-c, --queue, --endpoint) the commands
// under a resource each define. Every definition of a name binds the same
// field, so the value is the one given on the command that runs: a
// shadowed --org or --project (rejectShadowed refuses one that is set) and
// logs' and availability's own --project included.
type runFlags struct {
	output, debug string
	org, project  string
	version       bool
	group, queue  string
	endpoint      string
}

// newApp is a run with dependencies d.
func newApp(d *Deps) *App {
	return &App{
		Deps:    *d,
		printer: output.NewPrinter(d.Stdout),
		flags: runFlags{
			output:   "",
			debug:    "",
			org:      "",
			project:  "",
			version:  false,
			group:    "",
			queue:    "",
			endpoint: "",
		},
		debugLevel:    api.DebugOff,
		assumedWarned: atomic.Bool{},
		baseURLWarned: atomic.Bool{},
		treeErrs:      nil,
	}
}

// knownHostsDirMode is the permissions of the config directory defaultKnownHostsPath creates: the owner's only.
const knownHostsDirMode = 0o700

// defaultKnownHostsPath is the CLI's own known_hosts, beside the config file,
// so a relay port reused by another instance never touches the user's
// ~/.ssh/known_hosts. "" when the config directory cannot be determined; ssh
// then uses its default.
func defaultKnownHostsPath(env config.Env) string {
	p, err := config.Path(env)
	if err != nil {
		return ""
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, knownHostsDirMode); err != nil {
		return ""
	}
	return filepath.Join(dir, "known_hosts")
}
