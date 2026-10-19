package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// docs/commands.md is the command reference: every command it lists exists,
// and the binary has no undocumented command. Its alias table is the closed
// alias set (the alias rule in AGENTS.md). These tests tie both to the
// command tree.

var backtickSpan = regexp.MustCompile("`([^`]+)`")

// commandRows are the first cells of docs/commands.md's command tables, and
// aliasRows its alias table's rows, as {commands, aliases}.
func commandDocRows(t *testing.T) (commandRows []string, aliasRows [][2]string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "commands.md"))
	if err != nil {
		t.Fatal(err)
	}
	inAliases := false
	for line := range strings.SplitSeq(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			inAliases = line == "## Aliases"
		case strings.HasPrefix(line, "| `"):
			cells := strings.Split(strings.ReplaceAll(line, `\|`, "¦"), "|")
			if len(cells) < 3 {
				continue
			}
			if inAliases {
				aliasRows = append(aliasRows, [2]string{cells[1], cells[2]})
			} else {
				commandRows = append(commandRows, cells[1])
			}
		}
	}
	return commandRows, aliasRows
}

// documentedLeaves are the leaf commands the docs must cover: every runnable,
// visible leaf except cobra's own help and completion subcommands.
func (app *testRun) documentedLeaves() []*cobra.Command {
	var leaves []*cobra.Command
	walkCommands(app.root, func(c *cobra.Command) {
		if c.HasSubCommands() || !c.Runnable() || c.Hidden || c == app.root {
			return
		}
		if c.Name() == "help" || c.Parent() != nil && c.Parent().Name() == "completion" {
			return
		}
		leaves = append(leaves, c)
	})
	return leaves
}

// rowWords splits a cell's command spans into words, alternatives
// ("get/restart") into separate words.
func rowWords(cell string) map[string]bool {
	words := make(map[string]bool)
	for _, m := range backtickSpan.FindAllStringSubmatch(cell, -1) {
		for f := range strings.FieldsSeq(m[1]) {
			for w := range strings.SplitSeq(f, "/") {
				words[w] = true
			}
		}
	}
	return words
}

func TestEveryCommandIsDocumented(t *testing.T) {
	app := newTestRun(t)
	rows, _ := commandDocRows(t)
	for _, c := range app.documentedLeaves() {
		path := strings.TrimPrefix(c.CommandPath(), app.root.Name()+" ")
		parent := strings.TrimSuffix(strings.TrimSuffix(path, c.Name()), " ")
		found := false
		for _, cell := range rows {
			spans := backtickSpan.FindAllStringSubmatch(cell, -1)
			if len(spans) == 0 {
				continue
			}
			first := spans[0][1]
			if parent != "" && !strings.HasPrefix(first, parent+" ") ||
				parent == "" && !strings.HasPrefix(first+" ", c.Name()+" ") {
				continue
			}
			if rowWords(cell)[c.Name()] {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s has no row in docs/commands.md", path)
		}
	}
}

func TestEveryDocumentedCommandExists(t *testing.T) {
	app := newTestRun(t)
	app.root.InitDefaultHelpCmd() // cobra adds these two at Execute time
	app.root.InitDefaultCompletionCmd()
	rows, _ := commandDocRows(t)
	for _, cell := range rows {
		spans := backtickSpan.FindAllStringSubmatch(cell, -1)
		if len(spans) == 0 {
			continue
		}
		// The leading plain words of the first span name the command; a last
		// word with "/" lists alternatives under the same parent.
		var words []string
		for f := range strings.FieldsSeq(spans[0][1]) {
			if strings.ContainsAny(f[:1], "<[-(.¦") || strings.Contains(f, "|") || strings.Contains(f, "¦") {
				break
			}
			words = append(words, f)
		}
		if len(words) == 0 {
			continue
		}
		last := words[len(words)-1]
		for alt := range strings.SplitSeq(last, "/") {
			path := append(append([]string{}, words[:len(words)-1]...), alt)
			c, rest, err := app.root.Find(path)
			if err != nil || len(rest) > 0 || c.Name() != alt {
				t.Errorf(
					"docs/commands.md row %q names %q, which is not a command",
					strings.TrimSpace(cell),
					strings.Join(path, " "),
				)
			}
		}
	}
}

func TestAliasTableMatchesTheTree(t *testing.T) {
	app := newTestRun(t)
	_, aliasRows := commandDocRows(t)
	documented := make(map[string][]string)
	for _, row := range aliasRows {
		var aliases []string
		for _, m := range backtickSpan.FindAllStringSubmatch(row[1], -1) {
			aliases = append(aliases, m[1])
		}
		slices.Sort(aliases)
		for _, m := range backtickSpan.FindAllStringSubmatch(row[0], -1) {
			documented[m[1]] = aliases
		}
	}
	actual := make(map[string][]string)
	walkCommands(app.root, func(c *cobra.Command) {
		if len(c.Aliases) == 0 || c.Hidden {
			return
		}
		aliases := append([]string{}, c.Aliases...)
		slices.Sort(aliases)
		actual[strings.TrimPrefix(c.CommandPath(), app.root.Name()+" ")] = aliases
	})
	for path, aliases := range actual {
		if got := strings.Join(documented[path], ", "); got != strings.Join(aliases, ", ") {
			t.Errorf("%s answers to %v, but docs/commands.md lists %q", path, aliases, got)
		}
	}
	for path := range documented {
		if _, ok := actual[path]; !ok {
			t.Errorf("docs/commands.md lists aliases for %s, which has none", path)
		}
	}
}
