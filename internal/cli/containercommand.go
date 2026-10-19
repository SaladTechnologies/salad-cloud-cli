package cli

import (
	"io"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// maxCommandWords is the API's limit on container.command (the SDK's
// maxItems).
const maxCommandWords = 100

// underGitBash reports whether the CLI runs under Git Bash (MSYS), which
// sets MSYSTEM for the processes it starts. Only there does an argument get
// rewritten into a Windows path before the CLI sees it, so the rewrite checks
// (cp, --queue-path, command words) run only there and cannot misfire on
// Linux, macOS, PowerShell or cmd.
func underGitBash() bool { return procEnv.Getenv("MSYSTEM") != "" }

// msysCommandWord matches a command word Git Bash rewrote before the CLI saw
// it: a leading /path, or one after "=" (--prefix=/usr), becomes a Windows
// path. Git Bash rewrites whole path-like words, so a word with whitespace in
// it (a script for bash -c) is never checked: its text is literal.
var msysCommandWord = regexp.MustCompile(`(^|=)[A-Za-z]:[/\\]`)

// splitCommand separates the positionals of create and update from the
// container command after "--". given is true when "--" was on the line.
func splitCommand(cmd *cobra.Command, args []string) (pos, command []string, given bool) {
	if n := cmd.ArgsLenAtDash(); n >= 0 && n <= len(args) {
		return args[:n], args[n:], true
	}
	return args, nil, false
}

// validateCommand checks the container command before any file or API work.
// The words reach the platform as an array, where the first runs as the
// program in place of the image's ENTRYPOINT and CMD. The platform drops
// empty words and trims the spaces around each (docs/notes.md): a dropped
// word shifts the ones after it, so an empty word is rejected, while trimming
// is left alone (harmless for the usual bash -c '<script>').
func validateCommand(words []string, given bool) *clierr.Error {
	if !given {
		return nil
	}
	if len(words) == 0 {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage,
			"nothing after --").
			WithHint("give the container command, for example -- python app.py; it replaces the image's ENTRYPOINT and CMD")
	}
	if underGitBash() {
		for _, w := range words {
			if !strings.ContainsAny(w, " \t\n") && msysCommandWord.MatchString(w) {
				return clierr.New(
					clierr.CodeUsage,
					clierr.KindUsage,
					`command word "%s" was rewritten by Git Bash, which turns a leading / into a Windows path (also after =)`,
					w,
				).
					WithHint("run the command again with MSYS_NO_PATHCONV=1 in front of it, or from PowerShell or cmd")
			}
		}
	}
	if len(words) > maxCommandWords {
		return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
			"invalid command: %d words, the API allows at most %d", len(words), maxCommandWords).
			WithHint("put a longer script in the image or in bash -c '<script>'")
	}
	for i, w := range words {
		if w == "" {
			return clierr.New(
				clierr.CodeInvalidInput,
				clierr.KindInvalidInput,
				"invalid command: word %d is empty; the platform drops empty command words, which would shift the ones after it",
				i+1,
			)
		}
	}
	if strings.HasPrefix(words[0], "-") {
		return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
			`invalid command: it starts with "%s"`, words[0]).
			WithHint("start it with a program, for example -- python app.py --port 8080; the command replaces the image's ENTRYPOINT and CMD")
	}
	return nil
}

// warnCommand reports, on w, two likely mistakes validateCommand cannot
// reject without false alarms: a first word with whitespace in it (run as
// one program name, since there is no shell, unlike cg ssh), and words that
// are saladcloud's own flags (typed after --, they went into the container
// command instead of to saladcloud). Only the flags no program plausibly has
// count (saladcloudOnlyFlags): --port, -e, -f, --cpu and the like are common
// program flags, and -- python app.py --port 8080 must stay quiet.
func warnCommand(w io.Writer, cmd *cobra.Command, words []string) {
	if len(words) == 0 {
		return
	}
	if first := words[0]; strings.ContainsAny(first, " \t\n") {
		fprintf(
			w,
			"warning: the command's first word \"%s\" contains a space, so it runs as one program name; give the program and its arguments as separate words (-- sleep 2147483647, not -- \"sleep 2147483647\"); unlike cg ssh, there is no shell\n",
			first,
		)
	}
	own := commandFlagNames(cmd)
	var hits []string
	for _, word := range words[1:] {
		name, _, _ := strings.Cut(word, "=")
		if own[name] {
			hits = append(hits, word)
		}
	}
	if len(hits) > 0 {
		fprintf(
			w,
			"warning: %s after -- went into the container command, not to saladcloud; put saladcloud's own flags before --\n",
			quoteList(hits),
		)
	}
}

// saladcloudOnlyFlags are the create/update and global flags that no program
// plausibly takes, so after -- they are almost surely misplaced.
var saladcloudOnlyFlags = []string{
	"output", "org", "project", "debug",
	"wait", "no-wait", "timeout", "replicas", "image", "from",
	"queue", "queue-port", "queue-path", "no-autostart", "no-auth", "allow-unknown-fields",
}

// commandFlagNames is every spelling (--name and, where it has one, -x) of
// the saladcloudOnlyFlags cmd actually has, its own or inherited.
func commandFlagNames(cmd *cobra.Command) map[string]bool {
	names := make(map[string]bool)
	for _, n := range saladcloudOnlyFlags {
		f := cmd.Flags().Lookup(n)
		if f == nil {
			f = cmd.InheritedFlags().Lookup(n)
		}
		if f == nil {
			continue
		}
		names["--"+f.Name] = true
		if f.Shorthand != "" {
			names["-"+f.Shorthand] = true
		}
	}
	return names
}

// quoteList renders words as "a", "b" and "c".
func quoteList(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = `"` + w + `"`
	}
	if len(q) == 1 {
		return q[0]
	}
	return strings.Join(q[:len(q)-1], ", ") + " and " + q[len(q)-1]
}
