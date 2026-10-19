package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// console is the person at the keyboard, when there is one: whether stdin is
// a terminal, where the lines that exist only for a prompt's exchange go, and
// how an answer is read. Tests replace con with a scripted one, so every
// prompt path runs offline.
type console interface {
	// Terminal reports whether stdin is a terminal.
	Terminal() bool
	// Device is where prompts and the lines that exist only for the exchange
	// are written.
	Device() io.Writer
	// ReadHidden reads one answer with echo off.
	ReadHidden() (string, error)
	// ReadLine reads one answer, trimmed.
	ReadLine() (string, error)
}

// atTerminal reports whether a person can be asked: stdin is a terminal,
// SALAD_CLI_NO_PROMPT is unset or empty, and CI is unset, empty, false or 0.
// Otherwise the CLI behaves as if there were no terminal: no prompts, no
// single-choice pick, no browser offer, no organization names in hints, no
// removal steps under a variable warning.
func atTerminal() bool {
	if os.Getenv("SALAD_CLI_NO_PROMPT") != "" || inCI() {
		return false
	}
	return con.Terminal()
}

// inCI reports whether CI is set to anything but empty, false or 0, as
// GitHub Actions, GitLab and most CI systems set it.
func inCI() bool {
	v := os.Getenv("CI")
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

// errInterrupt is what a console read returns when the person presses
// Ctrl-C.
var errInterrupt = errors.New("interrupted")

// con is the console every prompt goes through.
var con console = &stdConsole{}

// openPage opens a URL in the user's browser. Tests replace it.
var openPage = openBrowser

// goos is the operating system the CLI runs on. Tests set it to check what
// another one is told.
var goos = runtime.GOOS

// stdConsole is the process's own terminal: answers come from stdin, prompts
// go to the terminal device, so they reach the person even when stdout and
// stderr are redirected (saladcloud login -o json 2>err.json).
type stdConsole struct {
	device io.Writer
	keys   *keyReader
	in     *bufio.Reader // for the rare terminal that cannot be put in raw mode
}

func (*stdConsole) Terminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// Device opens the terminal device once (/dev/tty; CONOUT$ on Windows),
// falling back to stderr when it cannot be opened.
func (c *stdConsole) Device() io.Writer {
	if c.device == nil {
		name := "/dev/tty"
		if runtime.GOOS == "windows" {
			name = "CONOUT$"
		}
		if f, err := os.OpenFile(name, os.O_WRONLY, 0); err == nil {
			c.device = f
		} else {
			c.device = os.Stderr
		}
	}
	return c.device
}

func (c *stdConsole) ReadHidden() (string, error) { return c.read(true) }
func (c *stdConsole) ReadLine() (string, error)   { return c.read(false) }

// read reads one answer with the terminal in raw mode, so Ctrl-C and end of
// input arrive as keys and end the read at once instead of a signal that
// leaves it waiting; the terminal is restored before read returns, whatever
// happened. Typed characters are echoed to the device unless hidden.
func (c *stdConsole) read(hidden bool) (string, error) {
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return c.readCooked(hidden)
	}
	defer func() {
		if err := term.Restore(fd, state); err != nil {
			fprintf(errw(), "warning: could not restore the terminal: %v\n", err)
		}
	}()
	echo := c.Device()
	if hidden {
		echo = io.Discard
	}
	if c.keys == nil {
		c.keys = &keyReader{r: os.Stdin}
	}
	answer, err := c.keys.readLine(echo)
	fprint(c.Device(), "\r\n") // raw mode does not turn \n into a new line
	return answer, err
}

// readCooked is read for a terminal that refused raw mode.
func (c *stdConsole) readCooked(hidden bool) (string, error) {
	if hidden {
		raw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fprintln(c.Device())
		return strings.TrimSpace(string(raw)), err
	}
	if c.in == nil {
		c.in = bufio.NewReader(os.Stdin)
	}
	return readLine(c.in)
}

// The keys readKeys acts on.
const (
	keyInterrupt = 0x03 // Ctrl-C
	keyEndUnix   = 0x04 // Ctrl-D
	keyEndWin    = 0x1a // Ctrl-Z
	keyBackspace = 0x7f
	keyCtrlH     = 0x08
)

// keyReader reads keystrokes from a terminal in raw mode, one line at a
// time.
type keyReader struct {
	r io.Reader
	// afterCR: the last line ended with \r, so a \n first thing in the next
	// one is the same Enter (a pasted line ending in \r\n), not an empty answer.
	afterCR bool
}

// readLine reads keystrokes up to Enter, and returns the line trimmed. Printable characters are kept and echoed;
// Backspace removes the last one; Ctrl-C returns errInterrupt; Ctrl-D or
// Ctrl-Z on an empty line, or the end of input, returns [io.EOF] (later in a
// line they do nothing). Other control
// characters (an arrow key's escape sequence, paste markers) are kept unechoed,
// for the caller's checks to judge.
func (k *keyReader) readLine(echo io.Writer) (string, error) {
	var line []rune
	var partial []byte // a UTF-8 character still arriving
	b := make([]byte, 1)
	skipLF := k.afterCR
	k.afterCR = false
	for {
		n, err := k.r.Read(b)
		if n == 0 {
			if err != nil {
				if len(line) == 0 {
					return "", io.EOF
				}
				return strings.TrimSpace(string(line)), nil
			}
			continue
		}
		c := b[0]
		if skipLF && c == '\n' {
			skipLF = false
			continue
		}
		skipLF = false
		switch {
		case c == keyInterrupt:
			return "", errInterrupt
		case c == keyEndUnix || c == keyEndWin:
			if len(line) == 0 {
				return "", io.EOF
			}
		case c == '\r' || c == '\n':
			k.afterCR = c == '\r'
			return strings.TrimSpace(string(line)), nil
		case c == keyBackspace || c == keyCtrlH:
			if len(line) > 0 {
				line = line[:len(line)-1]
				fprint(echo, "\b \b")
			}
		case c < 0x20:
			line = append(line, rune(c))
		default:
			partial = append(partial, c)
			if utf8.FullRune(partial) {
				ch, _ := utf8.DecodeRune(partial)
				line = append(line, ch)
				writeUnreported(echo, string(partial))
				partial = partial[:0]
			}
		}
	}
}

// readLine reads one line and trims surrounding whitespace. [io.EOF] with
// partial data still yields the data (a pipe without a trailing newline).
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if err != nil && (err != io.EOF || line == "") {
		return line, fmt.Errorf("reading a line: %w", err)
	}
	return line, nil
}
