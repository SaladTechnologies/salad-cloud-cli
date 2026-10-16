package cli

// The pieces the setup commands share (login, ai-gateway login, use, whoami,
// logout, doctor, config): how a key is shown and cleaned, where a value came
// from, what a name must look like, how a hint names the user's
// organizations, and the config file check that runs before any prompt.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
)

// stderrTee, when set, is where errw writes instead of [os.Stderr]: the test
// harness records stderr in order with stdout and the terminal.
var stderrTee io.Writer

// errw is where a command's stderr lines go.
func errw() io.Writer {
	if stderrTee != nil {
		return stderrTee
	}
	return os.Stderr
}

// say writes a readback or status line to stderr, in table mode only: in
// json and yaml mode stderr carries only warnings, notes and the error
// document, and the JSON document holds the fact.
func say(format string, args ...any) {
	if output.Mode() == "table" {
		fmt.Fprintf(errw(), format+"\n", args...)
	}
}

// hintLine writes a suggested next step after a success as a hint: line on
// stderr, in table mode only.
func hintLine(format string, args ...any) { say("hint: "+format, args...) }

// The two credentials, as messages name them.
const (
	labelAPIKey     = "API key"
	labelGatewayKey = "AI Gateway key"
)

// maskAPIKey shows a credential as "****..." and its last four characters
// ("****...ABCD"), or "(not set)" when empty. A key of eight characters or
// fewer is "****" alone, so the mask never shows half a key. A character
// outside printable ASCII among the four shows as "?", so the mask is always
// plain ASCII.
func maskAPIKey(k string) string {
	if k == "" {
		return "(not set)"
	}
	r := []rune(k)
	if len(r) <= 8 {
		return "****"
	}
	last := r[len(r)-4:]
	for i, c := range last {
		if c < 0x20 || c > 0x7e {
			last[i] = '?'
		}
	}
	return "****..." + string(last)
}

// fromTag is how a message says where a value came from, with a leading
// space: " (from --org)", " (from SALAD_ORGANIZATION)", " (from config)",
// " (from stdin)"; "" for a value typed at a prompt.
func fromTag(origin string) string {
	if origin == "" {
		return ""
	}
	return " (from " + origin + ")"
}

// originOf names a resolved value's source for fromTag: the flag, the
// variable, or "config" ("" for the built-in fallback).
func originOf(source, flag, env string) string {
	switch source {
	case "flag":
		return flag
	case "env":
		return env
	case "config":
		return "config"
	}
	return ""
}

// namePattern is the API's pattern for organization and project names.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// validName reports whether name fits the API's name pattern.
func validName(name string) bool { return namePattern.MatchString(name) }

// nameGuess says what a name that breaks the pattern most likely is, as a
// lowercase clause: a name that lowercases into a valid one gets "did you
// mean"; one with a space or another character no name has looks like a
// display name; anything else gets the rule. kind is "organization" or
// "project"; verb is how the display-name guess says to give the name ("use"
// in a hint, "type" at a prompt).
func nameGuess(kind, name, verb string) string {
	if lower := strings.ToLower(name); validName(lower) {
		return fmt.Sprintf(`did you mean "%s"?`, lower)
	}
	if strings.IndexFunc(name, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-')
	}) >= 0 {
		return fmt.Sprintf(
			"it looks like a display name; %s the name from the portal URL, the part after /%ss/",
			verb,
			kind,
		)
	}
	return "names are 2-63 characters: lowercase letters, digits and hyphens, starting with a letter and ending with a letter or digit"
}

// looksLikeKey reports whether a value in an organization or project slot
// may be a key put there by mistake: longer than any name can be, or equal
// to the API key or AI Gateway key in effect or saved. Such a value is
// never shown.
func looksLikeKey(v string) bool {
	if len(v) > 63 {
		return true
	}
	if v == "" {
		return false
	}
	saved, _ := config.Load()
	for _, k := range []string{os.Getenv(envAPIKey), os.Getenv(envGatewayKey), saved.APIKey, saved.GatewayAPIKey} {
		if key, _ := normalizeKey(k); key != "" && key == v {
			return true
		}
	}
	return false
}

// shownName is a name as messages show it: the name, or "hidden, looks
// like an API key" for a value that may be a key.
func shownName(v string) string {
	if looksLikeKey(v) {
		return "hidden, looks like an API key"
	}
	return v
}

// keyLikeName is the error for a value in an organization or project slot
// that looks like a key, by where it came from (origin: a flag, a variable,
// "config", or "" for use's argument). It never shows the value.
func keyLikeName(kind, origin string) *clierr.Error {
	what := article(kind) + " " + kind + " name"
	fix := fmt.Sprintf("the %s's name from the portal URL, the part after /%ss/", kind, kind)
	switch {
	case origin == "":
		return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "that looks like an API key, not %s", what).
			WithHint("the target is <org> or <org>/<project>, for example saladcloud use acme-prod/staging")
	case origin == "config":
		target := "<org>"
		if kind == "project" {
			target = "<org>/<project>"
		}
		return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "the saved %s looks like an API key, not %s", kind, what).
			WithHint("to switch, run saladcloud use %s", target)
	case strings.HasPrefix(origin, "--"):
		return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "%s looks like an API key, not %s", origin, what).
			WithHint("pass %s", fix)
	}
	return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "%s looks like an API key, not %s", origin, what).
		WithHint("set it to %s; the key goes in SALAD_API_KEY", fix)
}

// checkName is the error for an organization or project name that breaks
// the API's pattern, from a flag, a variable or use's argument (origin), or
// nil when it fits; a value that looks like a key gets keyLikeName's error
// instead, which does not show it. It runs before any request: the API
// would reject the name on every route.
func checkName(kind, name, origin string) *clierr.Error {
	if looksLikeKey(name) {
		return keyLikeName(kind, origin)
	}
	if validName(name) {
		return nil
	}
	return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, `invalid %s name "%s"%s`, kind, name, fromTag(origin)).
		WithHint("%s", nameGuess(kind, name, "use"))
}

// nameRetry is the line a prompt shows before asking again for a name that
// breaks the pattern. An answer that is the key just read, or too long to be
// a name, is never quoted back: it may be a key.
func nameRetry(kind, answer, key string) string {
	switch {
	case key != "" && answer == key:
		return fmt.Sprintf("That looks like the API key you just pasted, not %s %s name.", article(kind), kind)
	case len(answer) > 63:
		return fmt.Sprintf("That is not a valid %s name: names are at most 63 characters.", kind)
	}
	guess := nameGuess(kind, answer, "type")
	return fmt.Sprintf(
		`"%s" is not a valid %s name. %s%s`,
		answer,
		kind,
		strings.ToUpper(guess[:1]),
		guess[1:]+period(guess),
	)
}

// article is "an" before organization, "a" before project.
func article(word string) string {
	if strings.HasPrefix(word, "o") {
		return "an"
	}
	return "a"
}

// period is the full stop a sentence needs unless it ends in a question mark.
func period(s string) string {
	if strings.HasSuffix(s, "?") {
		return ""
	}
	return "."
}

// normalizeKey trims a key and strips what copying it adds: bracketed-paste
// markers and a UTF-8 byte order mark. Quotes are never stripped. ok is false
// when what is left has a character outside printable ASCII, which no key
// has.
func normalizeKey(raw string) (key string, ok bool) {
	key = strings.ReplaceAll(raw, "\x1b[200~", "")
	key = strings.ReplaceAll(key, "\x1b[201~", "")
	key = strings.TrimSpace(key)
	key = strings.TrimSpace(strings.TrimPrefix(key, "\xef\xbb\xbf"))
	for i := range len(key) {
		if key[i] < 0x20 || key[i] > 0x7e {
			return key, false
		}
	}
	return key, true
}

// quotedKey reports whether a key starts and ends with the same straight
// quote, as cmd's set VAR="..." and some .env files leave it.
func quotedKey(key string) bool {
	return len(key) >= 2 && (key[0] == '"' || key[0] == '\'') && key[len(key)-1] == key[0]
}

// malformedKey is the error for a key with a character no key has; label is
// labelAPIKey or labelGatewayKey, origin where the key came from.
func malformedKey(label, key, origin string) *clierr.Error {
	return clierr.New(clierr.CodeAuth, clierr.KindAuth, "%s %s%s contains characters an %s cannot have", label, maskAPIKey(key), fromTag(origin), label).
		WithHint("check for quotes or stray characters in it")
}

// malformedKeyRetry is malformedKey's line at a prompt, before asking again.
func malformedKeyRetry(label string) string {
	return fmt.Sprintf(
		"That %s contains characters an %s cannot have. Check for quotes or stray characters in it.",
		label,
		label,
	)
}

// orgListShown is how many organization names a hint lists before cutting
// the list short.
const orgListShown = 10

// orgsClause is the part of a hint that names the user's organizations. At a
// terminal: "your organizations: my-org, acme-prod", the first 10 then "and
// N more; to list all, run saladcloud orgs list"; "" when there are no names
// to show (names nil: the listing was unavailable). Off a terminal, where
// the reader is a log or an agent, it never lists them: "to list yours, run
// saladcloud orgs list", whatever names holds, so callers there need not
// fetch the listing for it.
func orgsClause(names []string) string {
	if !atTerminal() {
		return "to list yours, run saladcloud orgs list"
	}
	if len(names) == 0 {
		return ""
	}
	if len(names) <= orgListShown {
		return "your organizations: " + strings.Join(names, ", ")
	}
	return fmt.Sprintf("your organizations: %s and %d more; to list all, run saladcloud orgs list",
		strings.Join(names[:orgListShown], ", "), len(names)-orgListShown)
}

// hintClauses joins the non-empty clauses of a hint with "; ".
func hintClauses(clauses ...string) string {
	var out []string
	for _, c := range clauses {
		if c != "" {
			out = append(out, c)
		}
	}
	return strings.Join(out, "; ")
}

// configReady checks, before any prompt or request, that the config file can
// be read and saved, so a setup command never asks for what it cannot keep
// and never overwrites a corrupt file. command is the one the hint says to
// run again: "login", "ai-gateway login" or "use".
func configReady(command string) *clierr.Error {
	path, err := config.Path()
	if err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot find a folder for the config file: %s", config.Reason(err)).
			WithHint("%s", unwritableHint(command))
	}
	if _, err := config.Load(); err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot read the config file %s: %s", path, readReason(err)).
			WithHint("fix or delete it, then run saladcloud %s again", command)
	}
	if err := config.Writable(); err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot write the config file %s: %s", path, config.Reason(err)).
			WithHint("%s", unwritableHint(command))
	}
	return nil
}

// readReason is why the config file could not be read, without its path.
func readReason(err error) string {
	var re *config.ReadError
	if errors.As(err, &re) {
		return re.Reason()
	}
	return config.Reason(err)
}

// unwritableHint is the fix for a config file that cannot be saved: another
// folder, or the variables the command would have saved.
func unwritableHint(command string) string {
	switch command {
	case "login":
		return "set SALAD_CLI_CONFIG_DIR to a writable folder, or set SALAD_API_KEY, SALAD_ORGANIZATION and SALAD_PROJECT instead of logging in"
	case "ai-gateway login":
		return "set SALAD_CLI_CONFIG_DIR to a writable folder, or set SALAD_AI_GATEWAY_API_KEY instead of logging in"
	}
	return "set SALAD_CLI_CONFIG_DIR to a writable folder, then run saladcloud " + command + " again"
}

// sshMissingHint says how to get an OpenSSH client tool (ssh, ssh-keygen)
// on this OS.
func sshMissingHint(tool string) string {
	switch goos {
	case "windows":
		return "add the OpenSSH Client optional feature in Windows Settings, then open a new terminal"
	case "darwin":
		return fmt.Sprintf("macOS includes it at /usr/bin/%s; check that /usr/bin is on your PATH", tool)
	}
	return "install your distribution's OpenSSH client (openssh-client on Debian and Ubuntu, openssh-clients on Fedora)"
}

// isDedicatedKey reports whether a private key path is the CLI's own key.
func isDedicatedKey(priv string) bool { return filepath.Base(priv) == sshDedicatedKeyName }
