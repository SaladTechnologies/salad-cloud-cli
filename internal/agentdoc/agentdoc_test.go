package agentdoc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlockShape(t *testing.T) {
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
	noEnv := func(string) string { return "" }
	claude := func(k string) string {
		if k == EnvClaudeCode {
			return "1"
		}
		return ""
	}

	t.Run("default is AGENTS.md", func(t *testing.T) {
		dir := t.TempDir()
		got := PickFile(dir, "", noEnv)
		if got.Name != FileAgents || got.Reason != "default" || got.Path != filepath.Join(dir, FileAgents) {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("CLAUDECODE selects CLAUDE.md even when AGENTS.md exists", func(t *testing.T) {
		dir := t.TempDir()
		touch(t, filepath.Join(dir, FileAgents))
		got := PickFile(dir, "", claude)
		if got.Name != FileClaude || got.Reason != "CLAUDECODE set" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("existing CLAUDE.md alone selects CLAUDE.md", func(t *testing.T) {
		dir := t.TempDir()
		touch(t, filepath.Join(dir, FileClaude))
		got := PickFile(dir, "", noEnv)
		if got.Name != FileClaude || got.Reason != "CLAUDE.md present" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("both present selects AGENTS.md", func(t *testing.T) {
		dir := t.TempDir()
		touch(t, filepath.Join(dir, FileClaude))
		touch(t, filepath.Join(dir, FileAgents))
		got := PickFile(dir, "", noEnv)
		if got.Name != FileAgents {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("override relative", func(t *testing.T) {
		dir := t.TempDir()
		got := PickFile(dir, "docs/AGENTS.md", noEnv)
		if got.Path != filepath.Join(dir, "docs", "AGENTS.md") || got.Reason != "--file" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("override absolute", func(t *testing.T) {
		dir := t.TempDir()
		abs := filepath.Join(t.TempDir(), "X.md")
		got := PickFile(dir, abs, noEnv)
		if got.Path != abs {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("a directory named CLAUDE.md does not count", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, FileClaude), 0o750); err != nil {
			t.Fatal(err)
		}
		got := PickFile(dir, "", noEnv)
		if got.Name != FileAgents {
			t.Errorf("got %+v", got)
		}
	})
}

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
