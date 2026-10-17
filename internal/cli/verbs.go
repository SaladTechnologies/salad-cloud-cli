package cli

import (
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// maxSuggestions caps the did-you-mean list; beyond it the match says
// nothing useful and the bare unknown-command message stands.
const maxSuggestions = 4

// requireVerbs makes every command that only groups others (a resource
// noun, a product area) a usage error when run bare: exit 2 with the error
// document, never help on stdout with exit 0, which would make
// 'gpus -o json | jq' fail silently. Help stays on --help. An unknown word
// after the noun is an unknown command, with did-you-mean; flags the noun
// does not know are skipped, so a verb's own flags on a misplaced verb
// never hide the suggestion. Idempotent.
func requireVerbs(root *cobra.Command) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		if c == root || !c.HasSubCommands() || c.Runnable() {
			return
		}
		c.Args = cobra.ArbitraryArgs
		c.FParseErrWhitelist.UnknownFlags = true
		c.RunE = func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return withSuggestions(cmd, args[0], args[1:])
			}
			return clierr.New(clierr.CodeUsage, clierr.KindUsage, "%s needs a verb: %s",
				strings.TrimPrefix(cmd.CommandPath(), root.Name()+" "), strings.Join(verbsOf(cmd), ", "))
		}
	}
	walk(root)
}

// verbsOf lists c's available subcommands, in cobra's order.
func verbsOf(c *cobra.Command) []string {
	var names []string
	for _, sub := range c.Commands() {
		if sub.IsAvailableCommand() {
			names = append(names, sub.Name())
		}
	}
	return names
}

// commandsNamed returns the full paths of every command in the tree whose
// name or alias is word, or whose SuggestFor lists it (a guess such as
// 'status' that the command answers, without being an alias), in tree
// order. A leaf is its own path; a noun is
// followed by its subcommand named next when it has one ('saladcloud jobs
// list' finds 'queues jobs list'), and otherwise stands alone.
func commandsNamed(root *cobra.Command, word, next string) []string {
	var paths []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if !sub.IsAvailableCommand() {
				continue
			}
			if sub.Name() == word || sub.HasAlias(word) || slices.Contains(sub.SuggestFor, word) {
				path := sub.CommandPath()
				if next != "" {
					i := slices.IndexFunc(sub.Commands(), func(v *cobra.Command) bool {
						return v.IsAvailableCommand() && (v.Name() == next || v.HasAlias(next))
					})
					if i >= 0 {
						path += " " + sub.Commands()[i].Name()
					}
				}
				paths = append(paths, path)
			}
			walk(sub)
		}
	}
	walk(root)
	return paths
}

// nearNames returns the full paths of parent's subcommands nearest word by
// edit distance, within a small limit, for typos ('cg lst' finds 'cg list').
func nearNames(parent *cobra.Command, word string) []string {
	best := 2
	if len(word) <= 3 {
		best = 1
	}
	var paths []string
	for _, sub := range parent.Commands() {
		if !sub.IsAvailableCommand() {
			continue
		}
		d := best + 1
		for _, name := range append([]string{sub.Name()}, sub.Aliases...) {
			d = min(d, editDistance(strings.ToLower(word), name))
		}
		switch {
		case d < best:
			best, paths = d, append(paths[:0], sub.CommandPath())
		case d == best:
			paths = append(paths, sub.CommandPath())
		}
	}
	return paths
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// firstWord is the first argument that is not a flag, or "".
func firstWord(args []string) string {
	for _, a := range args {
		if a == "--" {
			return ""
		}
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// withSuggestions is the error for an unknown command typed under parent:
// when the word names one to maxSuggestions commands anywhere in the tree,
// its hint lists them on one line ("did you mean saladcloud
// container-groups create, saladcloud inference jobs create?"); failing
// that, near spellings among parent's own subcommands; otherwise there is
// no hint. This is the permanent, generic answer to a command spelled in
// the wrong place or mistyped, not a shim for renamed ones.
func withSuggestions(parent *cobra.Command, word string, rest []string) *clierr.Error {
	e := clierr.New(clierr.CodeUsage, clierr.KindUsage, `unknown command "%s" for %s`, word, parent.CommandPath())
	paths := commandsNamed(parent.Root(), word, firstWord(rest))
	if len(paths) == 0 {
		paths = nearNames(parent, word)
	}
	if len(paths) == 0 || len(paths) > maxSuggestions {
		return e
	}
	return e.WithHint("did you mean %s?", strings.Join(paths, ", "))
}

// unknownCommandRe matches cobra's unknown-command error.
var unknownCommandRe = regexp.MustCompile(`^unknown command "([^"]+)" for "[^"]+"`)

// suggestUnknownCommand applies withSuggestions to cobra's own
// unknown-command error at the root, using the words that followed the
// unknown one on the command line; any other message is a usage error as
// cobra wrote it.
func suggestUnknownCommand(root *cobra.Command, msg string, args []string) *clierr.Error {
	m := unknownCommandRe.FindStringSubmatch(msg)
	if m == nil {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "%s", msg)
	}
	var rest []string
	for i, a := range args {
		if a == m[1] {
			rest = args[i+1:]
			break
		}
	}
	return withSuggestions(root, m[1], rest)
}
