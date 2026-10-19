package cli

import (
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
	GenerateKey func(keyPath string) error
	// ProductionAPI is the API doctor reports on when no override is set.
	ProductionAPI string
	// LookPath and HasPassphrase are the system checks doctor's SSH row makes.
	LookPath      func(file string) (string, error)
	HasPassphrase func(keyPath string) (has, known bool)
}

// ProcessDeps is the running process as a run's dependencies.
func ProcessDeps() Deps {
	env := config.ProcessEnv()
	return Deps{
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
	}
}

// App is one run: its dependencies and the state the run builds up.
type App struct {
	Deps

	printer       *output.Printer
	debugLevel    api.DebugLevel
	assumedWarned atomic.Bool
	// treeErrs are the errors from building the command tree. Marking a flag
	// required or hidden, or giving it completion, fails only for a flag the
	// command does not define or completion given twice: a bug, which Run
	// reports before it runs anything.
	treeErrs []error
}

// newApp is a run with dependencies d.
func newApp(d Deps) *App {
	return &App{
		Deps:          d,
		printer:       output.NewPrinter(d.Stdout),
		debugLevel:    api.DebugOff,
		assumedWarned: atomic.Bool{},
		treeErrs:      nil,
	}
}

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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	return filepath.Join(dir, "known_hosts")
}
