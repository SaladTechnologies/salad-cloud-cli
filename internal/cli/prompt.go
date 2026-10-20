package cli

// Prompting at a terminal (only login, ai-gateway login and ssh-key setup
// prompt): every line of the exchange goes to the terminal device, never to
// stdout or stderr, so a run with -o json 2>err.json shows the prompt on
// screen and keeps it out of the file.

import (
	"fmt"
	"os/exec"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// tell writes one line of the exchange to the terminal device.
//
//nolint:goprintffuncname // AGENTS.md names tell as the prompt-exchange helper every prompting verb uses
func (app *App) tell(format string, args ...any) {
	fprintf(app.Console.Device(), format+"\n", args...)
}

// ask writes prompt to the terminal device and reads the answer. Ctrl-C
// returns errInterrupt and end of input [io.EOF]: both end the exchange
// (answerEnded), and the terminal is already restored.
func (app *App) ask(prompt string) (string, error) { return app.askWith(prompt, app.Console.ReadLine) }

// askHidden is ask with the answer hidden as it is typed: the key prompts.
func (app *App) askHidden(prompt string) (string, error) {
	return app.askWith(prompt, app.Console.ReadHidden)
}

// askWith writes prompt to the terminal device and reads the answer with read, a console read; ask and askHidden
// say how it ends.
func (app *App) askWith(prompt string, read func() (string, error)) (string, error) {
	fprint(app.Console.Device(), prompt+" ")
	answer, err := read()
	if err != nil {
		return answer, fmt.Errorf("reading the answer: %w", err)
	}
	return answer, nil
}

// askUntil asks until check accepts an answer. check returns true to accept,
// or a line to show before asking again ("" asks again silently), or an
// error that ends the exchange. A read error (Ctrl-C, end of input) is
// returned as is, for the caller's cancel line.
func (app *App) askUntil(prompt string, check func(answer string) (bool, string, *clierr.Error)) (string, error) {
	for {
		answer, err := app.ask(prompt)
		if err != nil {
			return "", err
		}
		ok, retry, cerr := check(answer)
		if cerr != nil {
			return "", cerr
		}
		if ok {
			return answer, nil
		}
		if retry != "" {
			app.tell("%s", retry)
		}
	}
}

// browserVisible reports whether a page the CLI opened could be seen: not
// in an SSH session (it would open on the far machine, or not at all), and on
// Linux only with a display. The key prompts offer a page only then, and only
// at a terminal.
func (app *App) browserVisible() bool {
	if app.Env.Getenv("SSH_CONNECTION") != "" || app.Env.Getenv("SSH_TTY") != "" {
		return false
	}
	if app.Env.GOOS == "linux" && app.Env.Getenv("DISPLAY") == "" && app.Env.Getenv("WAYLAND_DISPLAY") == "" {
		return false
	}
	return true
}

// keyPrompt is the key prompt, with the browser offer where a page could be
// seen.
func (app *App) keyPrompt() string {
	if app.browserVisible() {
		return "Paste it here, hidden (press Enter to open the page in a browser):"
	}
	return "Paste it here, hidden:"
}

// openOffered opens url after an empty Enter at a key prompt and says what
// happened; the caller asks again either way.
func (app *App) openOffered(url string) {
	if err := app.OpenPage(url); err != nil {
		app.tell("Could not open a browser; open %s yourself.", url)
		return
	}
	app.tell("Opening %s in your browser.", url)
}

// opener is the command that opens url in the user's browser: the system's
// opener, or under WSL Windows' own browser through cmd.exe, since a Linux
// opener there often has no browser to start and still reports success.
func opener(env config.Env, url string) (string, []string) {
	switch {
	case env.GOOS == setupGOOSWindows:
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	case env.GOOS == "darwin":
		return "open", []string{url}
	case env.Getenv("WSL_DISTRO_NAME") != "":
		return "cmd.exe", []string{"/c", "start", "", url}
	}
	return "xdg-open", []string{url}
}

// openBrowser starts env's opener for url. Callers treat a failure as
// non-fatal.
func openBrowser(env config.Env, url string) error {
	name, args := opener(env, url)
	cmd := exec.Command(name, args...) //nolint:gosec,noctx // the system's URL opener; it must outlive the run
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", name, err)
	}
	return nil
}
