package agentdoc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlockShape(t *testing.T) {
	t.Parallel()
	b := Block("1.2.3")
	if !strings.HasPrefix(b, "<!-- SALADCLOUD START (saladcloud agent init 1.2.3) -->\n") {
		t.Errorf("start marker wrong:\n%s", b)
	}
	if !strings.HasSuffix(b, "\n<!-- SALADCLOUD END -->\n") {
		t.Errorf("end marker wrong:\n%s", b)
	}
	if strings.Contains(b, "\r") {
		t.Error("Block must use LF line endings")
	}
	if n := strings.Count(b, "\n"); n > 27 {
		t.Errorf("block is %d lines; keep it short — depth belongs in the skills", n)
	}
	// Facts the block must carry: they are the CLI's public contract.
	for _, want := range []string{
		"saladcloud -o json",
		"1 error, 2 usage, 3 auth, 4 not found, 5 rate limited,\n  6 invalid input, 7 network, 10 not available",
		"An error is one JSON line on stderr whose\n  `hint` is the fix",
		"Setup belongs to the user.",
		"ask the\n  user to run it in their own terminal; never ask for a key in the chat",
		"Pass --org, and --project where a command\n  takes it, instead of running `use` or `config set`",
		"If a name is not found, ask the user which one; never pick from a list.",
		"SALAD_API_KEY", "SALAD_ORGANIZATION", "SALAD_PROJECT",
		"npx skills add https://docs.salad.com",
		"https://docs.salad.com/llms.txt",
		"`container-groups delete` is immediate and unconfirmed",
		"`saladcloud container-groups` (alias `cg`)",
		"`cg logs <group>`",
		"its parents are flags (`cg instances list -c <group>`)",
		"take the instance as `--instance`",
		"`saladcloud availability --gpu <class>` costs platform queries (one per class, one more for several): call " +
			"it once",
		"The CLI is a beta release",
		"https://github.com/SaladTechnologies/salad-cloud-cli/issues",
		"Never include credential values",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("block missing %q", want)
		}
	}
	// Never ship a credential-shaped string.
	if strings.Contains(strings.ToLower(b), "salad_cloud_user_") {
		t.Error("block must not contain anything resembling an API key")
	}
}

func TestApplyEmptyFile(t *testing.T) {
	t.Parallel()
	out, changed, err := Apply("", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("changed = false for an empty file")
	}
	if out != Block("1.0.0") {
		t.Errorf("empty file should become exactly the block, got:\n%s", out)
	}
}

func TestApplyAppendsAfterBlankLine(t *testing.T) {
	t.Parallel()
	existing := "# My project\n\nRun `make test`.\n"
	out, changed, err := Apply(existing, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("changed = false")
	}
	want := existing + "\n" + Block("1.0.0")
	if out != want {
		t.Errorf("append shape wrong.\nwant:\n%q\ngot:\n%q", want, out)
	}
}

func TestApplyAppendsNewlineWhenFileLacksOne(t *testing.T) {
	t.Parallel()
	existing := "# My project" // no trailing newline
	out, _, err := Apply(existing, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "# My project\n\n<!-- SALADCLOUD START") {
		t.Errorf("expected newline + blank line before block, got:\n%q", out)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	t.Parallel()
	first, _, err := Apply("# Repo\n", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	second, changed, err := Apply(first, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second run reported a change")
	}
	if second != first {
		t.Errorf("second run altered the file.\nfirst:\n%q\nsecond:\n%q", first, second)
	}
}

func TestApplyReplacesInPlacePreservingSurroundings(t *testing.T) {
	t.Parallel()
	before := "# Repo\n\nintro text\n\n"
	after := "\n## Other section\nkept.\n"
	old := before + StartMarker("0.9.0") + "\nold body that must disappear\n<!-- SALADCLOUD END -->\n" + after
	out, changed, err := Apply(old, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("version bump should count as a change")
	}
	want := before + Block("1.0.0") + after
	if out != want {
		t.Errorf("replace shape wrong.\nwant:\n%q\ngot:\n%q", want, out)
	}
	if strings.Contains(out, "old body") {
		t.Error("old body survived")
	}
	if strings.Count(out, "SALADCLOUD START") != 1 || strings.Count(out, "SALADCLOUD END") != 1 {
		t.Error("marker count drifted")
	}
}

func TestApplyPreservesCRLF(t *testing.T) {
	t.Parallel()
	existing := "# Repo\r\n\r\nWindows file.\r\n"
	out, _, err := Apply(existing, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Errorf("bare LF introduced into a CRLF file:\n%q", out)
	}
	// And a second run over the CRLF result is a no-op.
	again, changed, err := Apply(out, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if changed || again != out {
		t.Error("CRLF re-run was not idempotent")
	}
}

func TestApplyMalformed(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"start only":     "x\n<!-- SALADCLOUD START (saladcloud agent init 1.0.0) -->\nbody\n",
		"end only":       "x\n<!-- SALADCLOUD END -->\n",
		"end before":     "<!-- SALADCLOUD END -->\n<!-- SALADCLOUD START (saladcloud agent init 1.0.0) -->\n",
		"two blocks":     Block("1.0.0") + "\n" + Block("1.0.0"),
		"two end marks":  Block("1.0.0") + "\n<!-- SALADCLOUD END -->\n",
		"two start mark": "<!-- SALADCLOUD START (x) -->\n<!-- SALADCLOUD START (y) -->\n<!-- SALADCLOUD END -->\n",
	}
	for name, in := range cases {
		_, _, err := Apply(in, "1.0.0")
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v, want ErrMalformed", name, err)
		}
	}
}

func TestPickFile(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		files    []string // made in the directory
		dirs     []string // made in the directory
		override string
		env      map[string]string
		want     Target // its Path relative to the directory
	}{
		{
			name: "default is AGENTS.md",
			want: Target{Path: FileAgents, Name: FileAgents, Reason: "default"},
		},
		{
			name:  "CLAUDECODE selects CLAUDE.md even when AGENTS.md exists",
			files: []string{FileAgents},
			env:   map[string]string{EnvClaudeCode: "1"},
			want:  Target{Path: FileClaude, Name: FileClaude, Reason: "CLAUDECODE set"},
		},
		{
			name:  "existing CLAUDE.md alone selects CLAUDE.md",
			files: []string{FileClaude},
			want:  Target{Path: FileClaude, Name: FileClaude, Reason: "CLAUDE.md present"},
		},
		{
			name:  "both present selects AGENTS.md",
			files: []string{FileClaude, FileAgents},
			want:  Target{Path: FileAgents, Name: FileAgents, Reason: "default"},
		},
		{
			name:     "override relative",
			override: "docs/AGENTS.md",
			want:     Target{Path: filepath.Join("docs", "AGENTS.md"), Name: "docs/AGENTS.md", Reason: "--file"},
		},
		{
			name: "a directory named CLAUDE.md does not count",
			dirs: []string{FileClaude},
			want: Target{Path: FileAgents, Name: FileAgents, Reason: "default"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			populate(t, dir, tc.files, tc.dirs)
			want := tc.want
			want.Path = filepath.Join(dir, want.Path)
			if got := PickFile(dir, tc.override, func(k string) string { return tc.env[k] }); got != want {
				t.Errorf("got %+v, want %+v", got, want)
			}
		})
	}
}

// populate makes the files and directories named in dir.
func populate(t *testing.T, dir string, files, dirs []string) {
	t.Helper()
	for _, f := range files {
		touch(t, filepath.Join(dir, f))
	}
	for _, d := range dirs {
		if err := os.Mkdir(filepath.Join(dir, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
}

// TestPickFileAbsoluteOverride: an absolute --file is used as it is, not joined to the directory.
func TestPickFileAbsoluteOverride(t *testing.T) {
	t.Parallel()
	abs := filepath.Join(t.TempDir(), "X.md")
	got := PickFile(t.TempDir(), abs, func(string) string { return "" })
	if want := (Target{Path: abs, Name: abs, Reason: "--file"}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
