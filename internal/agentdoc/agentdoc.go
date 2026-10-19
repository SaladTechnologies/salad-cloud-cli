// Package agentdoc renders and merges the SaladCloud block that
// `saladcloud agent init` writes into a project's AGENTS.md or CLAUDE.md.
//
// The block is delimited by HTML comment markers so a re-run replaces the
// previous block in place and never touches the rest of the file. The
// content deliberately carries only facts that are stable across releases —
// the CLI's contracts and the platform's shape — and points at the skills
// for everything that changes. Nothing in it is account state.
package agentdoc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/saladtechnologies/salad-cloud-cli/internal/version"
)

const (
	startPrefix = "<!-- SALADCLOUD START"
	endMarker   = "<!-- SALADCLOUD END -->"

	// FileAgents and FileClaude are the two files agent init knows how to
	// target. AGENTS.md is the cross-agent convention (Codex, Cursor,
	// Copilot, Gemini CLI, …); Claude Code reads CLAUDE.md instead.
	FileAgents = "AGENTS.md"
	// FileClaude is the file Claude Code reads instead of FileAgents.
	FileClaude = "CLAUDE.md"

	// EnvClaudeCode is set in the environment of every process Claude Code
	// spawns. Its presence means the caller is Claude Code, which will read
	// CLAUDE.md and ignore AGENTS.md.
	EnvClaudeCode = "CLAUDECODE"
)

// ErrMalformed is returned (wrapped) when the existing file has a start
// marker without an end, an end without a start, an end before a start, or
// more than one block. The caller should surface it as invalid input; the
// fix is manual because guessing which text to replace could destroy the
// user's own content.
var ErrMalformed = errors.New("malformed SaladCloud block")

// StartMarker returns the opening marker for a block written by the given
// CLI version. The version is informational: detection matches any
// "<!-- SALADCLOUD START" line, so an older block is still replaced.
func StartMarker(cliVersion string) string {
	return fmt.Sprintf("%s (saladcloud agent init %s) -->", startPrefix, cliVersion)
}

// body is the block content between the markers. Every line is a fact from
// the contract section of docs/guide.md or the platform's documented shape; keep
// it that way. Guidance that evolves belongs in the skills the last bullet
// installs, not here.
const body = `## SaladCloud
- Workloads run as container groups on interruptible consumer GPU nodes: local disk is ephemeral, any instance
  can be reallocated at any time, and inbound traffic reaches one port through the container gateway.
- Use the ` + "`saladcloud`" + ` CLI, not raw HTTP: ` + "`saladcloud -o json <command>`" + `. An error is one JSON line on stderr whose
  ` + "`hint`" + ` is the fix; ` + "`--help`" + ` is the reference. Exit codes: 1 error, 2 usage, 3 auth, 4 not found, 5 rate limited,
  6 invalid input, 7 network, 10 not available, or the child's from ` + "`cg ssh/sftp/cp`" + ` (a one-shot ` + "`cg ssh -- <cmd>`" + ` exits 1 even on success).
- Setup belongs to the user. When a hint says to run ` + "`saladcloud login`" + `, ` + "`ai-gateway login`" + ` or ` + "`ssh-key setup`" + `, ask the
  user to run it in their own terminal; never ask for a key in the chat. Pass --org, and --project where a command
  takes it, instead of running ` + "`use`" + ` or ` + "`config set`" + `. If a name is not found, ask the user which one; never pick from a list.
- Auth resolves flag > environment > config file: SALAD_API_KEY, SALAD_ORGANIZATION, SALAD_PROJECT (unset, the project
  is "default", with a stderr warning; set it). Never print their values. ` + "`saladcloud whoami`" + ` shows what resolved and where from.
- Container groups live under ` + "`saladcloud container-groups`" + ` (alias ` + "`cg`" + `). A command's target is its
  one positional and its parents are flags (` + "`cg instances restart <id> -c <group>`" + `). One group's
  logs: ` + "`cg logs <group>`" + `; ` + "`saladcloud logs`" + ` alone searches the organization.
  ` + "`saladcloud availability --gpu <class>`" + ` costs platform queries (one per class, one more for several): call it once before ` + "`cg create`" + `.
- Writes verify themselves: read the ` + "`warning:`" + `/` + "`note:`" + ` stderr lines after ` + "`cg create/update/scale`" + `.
  ` + "`container-groups delete`" + ` is immediate and unconfirmed; confirm with the user first.
- A local run to check ports, probes and auth is much faster than testing them on SaladCloud, where each try
  pushes the image to a registry, allocates a node and pulls the whole image there before it starts. To debug startup, ` + "`cg create <group> --image <image> --wait -- sleep 2147483647`" + `
  keeps the container up without its entrypoint; ` + "`cg ssh <group>`" + ` gets a shell; it runs until you delete it; ` + "`cg instances list -c <group>`" + ` shows pull progress.
- Before non-trivial work, install the SaladCloud skills: ` + "`npx skills add https://docs.salad.com`" + ` (index: https://docs.salad.com/llms.txt).
- The CLI is a ` + version.Stage + ` release. When it misbehaves (a crash, a wrong exit code, output that
  contradicts its own --help), tell the user and offer to draft a report for ` + version.IssuesURL + `
  with ` + "`saladcloud version`" + ` output, the command, and the stderr error line. Never include credential values.`

// Body returns the block content without markers.
func Body() string { return body }

// Block returns the complete block (start marker, body, end marker) with
// LF line endings and a trailing newline.
func Block(cliVersion string) string {
	return StartMarker(cliVersion) + "\n" + body + "\n" + endMarker + "\n"
}

// Apply merges the block for cliVersion into existing and reports whether the
// result differs from existing. An existing block (any version) is replaced
// in place; otherwise the block is appended after a blank line. Line endings
// follow the existing file: if it contains CRLF anywhere, the block is
// written with CRLF too.
func Apply(existing, cliVersion string) (result string, changed bool, err error) {
	block, nl := Block(cliVersion), "\n"
	if strings.Contains(existing, "\r\n") {
		block, nl = strings.ReplaceAll(block, "\n", "\r\n"), "\r\n"
	}

	start := strings.Index(existing, startPrefix)
	end := strings.Index(existing, endMarker)

	if start < 0 && end < 0 {
		// No block: append.
		out := appendBlock(existing, block, nl)
		return out, out != existing, nil
	}
	if err := checkMarkers(existing, start, end); err != nil {
		return "", false, err
	}

	// Replace [start, end+len(endMarker)] plus the line ending that follows
	// the end marker, if any, so the block's own trailing newline is not
	// doubled on every run.
	out := existing[:start] + block + trimLineEnding(existing[end+len(endMarker):])
	return out, out != existing, nil
}

// appendBlock is existing with block after a blank line, existing's last line ended first, each line ending nl; an
// empty existing becomes the block alone.
func appendBlock(existing, block, nl string) string {
	if existing == "" {
		return block
	}
	out := existing
	if !strings.HasSuffix(out, nl) {
		out += nl
	}
	return out + nl + block
}

// checkMarkers returns ErrMalformed unless existing holds exactly one block, its start marker at start and its end
// marker at end, at least one of them found.
func checkMarkers(existing string, start, end int) error {
	switch {
	case start < 0:
		return fmt.Errorf("%w: end marker without a start marker", ErrMalformed)
	case end < 0:
		return fmt.Errorf("%w: start marker without an end marker", ErrMalformed)
	case end < start:
		return fmt.Errorf("%w: end marker appears before the start marker", ErrMalformed)
	}
	if strings.Contains(existing[start+len(startPrefix):], startPrefix) ||
		strings.Contains(existing[end+len(endMarker):], endMarker) {
		return fmt.Errorf("%w: more than one block; remove the extras and re-run", ErrMalformed)
	}
	return nil
}

// trimLineEnding drops the one line ending, CRLF or LF, that tail starts with.
func trimLineEnding(tail string) string {
	switch {
	case strings.HasPrefix(tail, "\r\n"):
		return tail[2:]
	case strings.HasPrefix(tail, "\n"):
		return tail[1:]
	}
	return tail
}

// Target names the file agent init will write and why.
type Target struct {
	Path   string // full path, dir joined with the file name
	Name   string // AGENTS.md or CLAUDE.md (or the --file value)
	Reason string // "--file", "CLAUDECODE set", "CLAUDE.md present", or "default"
}

// PickFile chooses the target file in dir. override (from --file) wins;
// otherwise CLAUDE.md is chosen when the caller is Claude Code or when the
// project already has a CLAUDE.md and no AGENTS.md; otherwise AGENTS.md.
// getenv is injected so tests need not touch the process environment.
func PickFile(dir, override string, getenv func(string) string) Target {
	if override != "" {
		p := override
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		return Target{Path: p, Name: override, Reason: "--file"}
	}
	if getenv(EnvClaudeCode) != "" {
		return Target{Path: filepath.Join(dir, FileClaude), Name: FileClaude, Reason: EnvClaudeCode + " set"}
	}
	if exists(filepath.Join(dir, FileClaude)) && !exists(filepath.Join(dir, FileAgents)) {
		return Target{Path: filepath.Join(dir, FileClaude), Name: FileClaude, Reason: FileClaude + " present"}
	}
	return Target{Path: filepath.Join(dir, FileAgents), Name: FileAgents, Reason: "default"}
}

func exists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
