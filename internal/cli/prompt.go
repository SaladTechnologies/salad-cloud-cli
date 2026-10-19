package cli

// Prompting at a terminal (only login, ai-gateway login and ssh-key setup
// prompt): every line of the exchange goes to the terminal device, never to
// stdout or stderr, so a run with -o json 2>err.json shows the prompt on
// screen and keeps it out of the file.

import (
	"os"
	"os/exec"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// tell writes one line of the exchange to the terminal device.
func tell(format string, args ...any) {
	fprintf(con.Device(), format+"\n", args...)
}

// ask writes prompt to the terminal device and reads the answer, hidden when
// asked. Ctrl-C returns errInterrupt and end of input [io.EOF]: both end the
// exchange (answerEnded), and the terminal is already restored.
func ask(prompt string, hidden bool) (string, error) {
	fprint(con.Device(), prompt+" ")
	if hidden {
		return con.ReadHidden()
	}
	return con.ReadLine()
}

// askUntil asks until check accepts an answer. check returns true to accept,
// or a line to show before asking again ("" asks again silently), or an
// error that ends the exchange. A read error (Ctrl-C, end of input) is
// returned as is, for the caller's cancel line.
func askUntil(prompt string, hidden bool, check func(answer string) (bool, string, *clierr.Error)) (string, error) {
	for {
		answer, err := ask(prompt, hidden)
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
			tell("%s", retry)
		}
	}
}

// browserVisible reports whether a page the CLI opened could be seen: not
// in an SSH session (it would open on the far machine, or not at all), and on
// Linux only with a display. The key prompts offer a page only then, and only
// at a terminal.
func browserVisible() bool {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return false
	}
	if goos == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return false
	}
	return true
}

// keyPrompt is the key prompt, with the browser offer where a page could be
// seen.
func keyPrompt() string {
	if browserVisible() {
		return "Paste it here, hidden (press Enter to open the page in a browser):"
	}
	return "Paste it here, hidden:"
}

// openOffered opens url after an empty Enter at a key prompt and says what
// happened; the caller asks again either way.
func openOffered(url string) {
	if err := openPage(url); err != nil {
		tell("Could not open a browser; open %s yourself.", url)
		return
	}
	tell("Opening %s in your browser.", url)
}

// opener is the command that opens url in the user's browser: the system's
// opener, or under WSL Windows' own browser through cmd.exe, since a Linux
// opener there often has no browser to start and still reports success.
func opener(url string) (name string, args []string) {
	switch {
	case goos == "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	case goos == "darwin":
		return "open", []string{url}
	case os.Getenv("WSL_DISTRO_NAME") != "":
		return "cmd.exe", []string{"/c", "start", "", url}
	}
	return "xdg-open", []string{url}
}

// openBrowser starts the opener for url. Callers treat a failure as
// non-fatal.
func openBrowser(url string) error {
	name, args := opener(url)
	return exec.Command(name, args...).Start()
}
