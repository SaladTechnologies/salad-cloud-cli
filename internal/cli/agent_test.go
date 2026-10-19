package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/agentdoc"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// runAgentInitCmd executes `saladcloud agent init <args>` through the real
// command tree with stdout captured.
func (app *testRun) runAgentInitCmd(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	return app.runRoot(t, append([]string{"agent", "init"}, args...)...)
}

func TestAgentInitResultStableJSONShape(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(agentInitResult{Action: "printed", Reason: "--print", Version: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"file", "action", "ok", "reason", "version"} {
		if _, ok := m[k]; !ok {
			t.Errorf("key %q missing — every key must always be present", k)
		}
	}
	if string(m["file"]) != "null" {
		t.Errorf("file = %s, want null when nothing was written", m["file"])
	}
	if len(m) != 5 {
		t.Errorf("got %d keys, want 5", len(m))
	}
}

func TestAgentInitCreatesUpdatesUnchanged(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	app.setenv(agentdoc.EnvClaudeCode, "")
	dir := t.TempDir()

	// 1. Fresh directory: creates AGENTS.md.
	res := app.agentInitJSON(t, "first run", dir)
	if res.Action != "created" || res.Reason != "default" || res.File == nil ||
		filepath.Base(*res.File) != agentdoc.FileAgents {
		t.Errorf("first run = %+v", res)
	}
	content := must(os.ReadFile(filepath.Join(dir, agentdoc.FileAgents)))(t)
	if string(content) != agentdoc.Block(res.Version) {
		t.Errorf("created file is not exactly the block:\n%s", content)
	}

	// 2. Same again: unchanged, file byte-identical, exit 0.
	res = app.agentInitJSON(t, "second run", dir)
	if res.Action != "unchanged" {
		t.Errorf("second run action = %q, want unchanged", res.Action)
	}
	again := must(os.ReadFile(filepath.Join(dir, agentdoc.FileAgents)))(t)
	if !bytes.Equal(again, content) {
		t.Error("second run modified the file")
	}

	// 3. User edits above and below the block, block gets stale: updated,
	//    user text preserved.
	stale := "# Project\n\nUser intro.\n\n" +
		agentdoc.StartMarker("0.0.1") + "\nstale\n<!-- SALADCLOUD END -->\n" +
		"\n## Testing\nUser outro.\n"
	noErr(t, os.WriteFile(filepath.Join(dir, agentdoc.FileAgents), []byte(stale), 0o600))
	res = app.agentInitJSON(t, "third run", dir)
	if res.Action != "updated" {
		t.Errorf("third run action = %q, want updated", res.Action)
	}
	final := must(os.ReadFile(filepath.Join(dir, agentdoc.FileAgents)))(t)
	checkStaleBlockReplaced(t, string(final), "# Project\n\nUser intro.\n\n", "\n## Testing\nUser outro.\n")
}

// agentInitJSON runs agent init in dir with -o json and decodes its result; step names the run in failures.
func (app *testRun) agentInitJSON(t *testing.T, step, dir string) agentInitResult {
	t.Helper()
	out, err := app.runAgentInitCmd(t, "--dir", dir, "--print=false", "--file=", "-o", "json")
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
	var res agentInitResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%s output is not JSON: %v\n%s", step, err, out)
	}
	return res
}

// checkStaleBlockReplaced checks that the file s keeps the user's text around the block and holds one current
// block, none of the stale one.
func checkStaleBlockReplaced(t *testing.T, s string, keep ...string) {
	t.Helper()
	for _, k := range keep {
		if !strings.Contains(s, k) {
			t.Errorf("user text %q lost:\n%s", k, s)
		}
	}
	if strings.Contains(s, "stale") {
		t.Error("stale block body survived")
	}
	if strings.Count(s, "SALADCLOUD START") != 1 {
		t.Error("marker count drifted")
	}
}

func TestAgentInitClaudeCodeTargetsCLAUDEmd(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	app.setenv(agentdoc.EnvClaudeCode, "1")
	dir := t.TempDir()
	out, err := app.runAgentInitCmd(t, "--dir", dir, "--print=false", "--file=", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var res agentInitResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.File == nil || filepath.Base(*res.File) != agentdoc.FileClaude || res.Reason != "CLAUDECODE set" {
		t.Errorf("got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, agentdoc.FileClaude)); err != nil {
		t.Errorf("CLAUDE.md not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, agentdoc.FileAgents)); err == nil {
		t.Error("AGENTS.md must not be written under Claude Code")
	}
}

func TestAgentInitPrintWritesNothing(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	app.setenv(agentdoc.EnvClaudeCode, "")
	dir := t.TempDir()
	out, err := app.runAgentInitCmd(t, "--dir", dir, "--print", "--file=", "-o", "table")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "<!-- SALADCLOUD START") || !strings.HasSuffix(out, "<!-- SALADCLOUD END -->\n") {
		t.Errorf("--print should emit exactly the block:\n%s", out)
	}
	entries := must(os.ReadDir(dir))(t)
	if len(entries) != 0 {
		t.Errorf("--print wrote files: %v", entries)
	}

	out, err = app.runAgentInitCmd(t, "--dir", dir, "--print", "--file=", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var res agentInitResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.File != nil || res.Action != "printed" || res.Reason != "--print" {
		t.Errorf("json --print = %+v", res)
	}
}

func TestAgentInitMalformedIsInvalidInput(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	app.setenv(agentdoc.EnvClaudeCode, "")
	dir := t.TempDir()
	broken := "# Repo\n<!-- SALADCLOUD START (saladcloud agent init 1.0.0) -->\nno end marker\n"
	p := filepath.Join(dir, agentdoc.FileAgents)
	if err := os.WriteFile(p, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := app.runAgentInitCmd(t, "--dir", dir, "--print=false", "--file=", "-o", "json")
	var ce *clierr.Error
	if err == nil || !asClierr(err, &ce) || ce.Code != clierr.CodeInvalidInput || ce.Kind != clierr.KindInvalidInput {
		t.Fatalf("err = %v, want invalid_input exit 6", err)
	}
	after := must(os.ReadFile(p))(t)
	if string(after) != broken {
		t.Error("a malformed file must be left untouched")
	}
}

func TestAgentInitFileOverrideCreatesParentDir(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	app.setenv(agentdoc.EnvClaudeCode, "")
	dir := t.TempDir()
	out, err := app.runAgentInitCmd(t, "--dir", dir, "--print=false", "--file", "docs/AGENTS.md", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var res agentInitResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Reason != "--file" || res.Action != "created" {
		t.Errorf("got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "docs", "AGENTS.md")); err != nil {
		t.Errorf("override target not written: %v", err)
	}
}

func TestAgentInitBadDir(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	_, err := app.runAgentInitCmd(
		t,
		"--dir",
		filepath.Join(t.TempDir(), "missing"),
		"--print=false",
		"--file=",
		"-o",
		"json",
	)
	var ce *clierr.Error
	if err == nil || !asClierr(err, &ce) || ce.Code != clierr.CodeInvalidInput {
		t.Fatalf("err = %v, want invalid_input exit 6", err)
	}
}

func asClierr(err error, target **clierr.Error) bool {
	var ce *clierr.Error
	ok := errors.As(err, &ce)
	if ok {
		*target = ce
	}
	return ok
}
