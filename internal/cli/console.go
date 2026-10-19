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
// how an answer is read. Tests replace app.Console with a scripted one, so every
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
func (app *App) atTerminal() bool {
	if app.Env.Getenv("SALAD_CLI_NO_PROMPT") != "" || app.inCI() {
		return false
	}
	return app.Console.Terminal()
}

// inCI reports whether CI is set to anything but empty, false or 0, as
// GitHub Actions, GitLab and most CI systems set it.
func (app *App) inCI() bool {
	v := app.Env.Getenv("CI")
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

// errInterrupt is what a console read returns when the person presses
// Ctrl-C.
var errInterrupt = errors.New("interrupted")

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
		if runtime.GOOS == setupGOOSWindows {
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

func (c *stdConsole) ReadHidden() (string, error) { return c.read(noEcho, c.readCookedHidden) }
func (c *stdConsole) ReadLine() (string, error)   { return c.read(c.Device, c.readCookedLine) }

// noEcho is where a hidden answer's typed characters are echoed: nowhere.
func noEcho() io.Writer { return io.Discard }

// read reads one answer with the terminal in raw mode, so Ctrl-C and end of
// input arrive as keys and end the read at once instead of a signal that
// leaves it waiting; the terminal is restored before read returns, whatever
// happened. Typed characters are echoed to the writer echoTo returns; a
// terminal that refuses raw mode is read by cooked instead.
func (c *stdConsole) read(echoTo func() io.Writer, cooked func() (string, error)) (string, error) {
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return cooked()
	}
	defer func() {
		if restoreErr := term.Restore(fd, state); restoreErr != nil {
			fprintf(os.Stderr, "warning: could not restore the terminal: %v\n", restoreErr)
		}
	}()
	echo := echoTo()
	if c.keys == nil {
		c.keys = &keyReader{r: os.Stdin, afterCR: false}
	}
	answer, err := c.keys.readLine(echo)
	fprint(c.Device(), "\r\n") // raw mode does not turn \n into a new line
	return answer, err
}

// readCookedHidden is ReadHidden for a terminal that refused raw mode.
func (c *stdConsole) readCookedHidden() (string, error) {
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fprintln(c.Device())
	return strings.TrimSpace(string(raw)), err
}

// readCookedLine is ReadLine for a terminal that refused raw mode.
func (c *stdConsole) readCookedLine() (string, error) {
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
	keySpace     = 0x20 // the first printable character; those below it are control characters
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
	var l keyLine
	b := make([]byte, 1)
	skipLF := k.afterCR
	k.afterCR = false
	for {
		n, err := k.r.Read(b)
		if n == 0 {
			if err != nil {
				return l.atEnd()
			}
			continue
		}
		c := b[0]
		if skipLF && c == '\n' {
			skipLF = false
			continue
		}
		skipLF = false
		if done, line, err := k.press(&l, c, echo); done {
			return line, err
		}
	}
}

// keyLine is the line keyReader.readLine is reading.
type keyLine struct {
	runes   []rune
	partial []byte // a UTF-8 character still arriving
}

// press acts on one keystroke c, as readLine says; it reports whether the line has ended, with the line or the
// error that ended it.
func (k *keyReader) press(l *keyLine, c byte, echo io.Writer) (bool, string, error) {
	switch {
	case c == keyInterrupt:
		return true, "", errInterrupt
	case isEndOfInputKey(c):
		if len(l.runes) == 0 {
			return true, "", io.EOF
		}
	case c == '\r' || c == '\n':
		k.afterCR = c == '\r'
		return true, strings.TrimSpace(string(l.runes)), nil
	case isBackspaceKey(c):
		l.backspace(echo)
	case c < keySpace:
		l.runes = append(l.runes, rune(c))
	default:
		l.add(c, echo)
	}
	return false, "", nil
}

// isEndOfInputKey reports whether c ends input: Ctrl-D, or Ctrl-Z as Windows spells it.
func isEndOfInputKey(c byte) bool { return c == keyEndUnix || c == keyEndWin }

// isBackspaceKey reports whether c is Backspace, as the terminal sends it (DEL or Ctrl-H).
func isBackspaceKey(c byte) bool { return c == keyBackspace || c == keyCtrlH }

// atEnd is the line when input ends: [io.EOF] when it is empty, else the line trimmed.
func (l *keyLine) atEnd() (string, error) {
	if len(l.runes) == 0 {
		return "", io.EOF
	}
	return strings.TrimSpace(string(l.runes)), nil
}

// backspace removes the last character, from the line and the echo, if there is one.
func (l *keyLine) backspace(echo io.Writer) {
	if len(l.runes) > 0 {
		l.runes = l.runes[:len(l.runes)-1]
		fprint(echo, "\b \b")
	}
}

// add takes one byte of a printable character, which is kept and echoed once all its bytes have arrived.
func (l *keyLine) add(c byte, echo io.Writer) {
	l.partial = append(l.partial, c)
	if utf8.FullRune(l.partial) {
		ch, _ := utf8.DecodeRune(l.partial)
		l.runes = append(l.runes, ch)
		writeUnreported(echo, string(l.partial))
		l.partial = l.partial[:0]
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
