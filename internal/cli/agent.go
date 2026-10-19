package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/agentdoc"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/version"
)

// agentInitResult is agent init's JSON contract. Every key is always
// present. File is null only with --print, when nothing was written.
type agentInitResult struct {
	File    *string `json:"file"`
	Action  string  `json:"action"` // "created", "updated", "unchanged", "printed"
	OK      bool    `json:"ok"`     // always true on success, as on every action result
	Reason  string  `json:"reason"` // why this file: "--file", "CLAUDECODE set", "CLAUDE.md present", "default", or "--print"
	Version string  `json:"version"`
}

// addAgentCommands adds agent.go's commands to the tree.
func (app *App) addAgentCommands(root *cobra.Command) {
	agentCmd := &cobra.Command{
		Use:   "agent",
		Short: "Set up AI coding agents to work with SaladCloud",
		Long: `Commands that prepare a project or an agent to use SaladCloud through
this CLI. They never call the SaladCloud API and need no credentials.`,
	}

	var o agentInitOptions
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Write the SaladCloud block into the project's AGENTS.md or CLAUDE.md",
		Long: `Write a short, marker-delimited SaladCloud section into the file coding
agents read for project instructions, so an agent working in this repository
knows to use the CLI, how it authenticates, its exit codes, and where the
skills are.

Target file: --file if given; otherwise CLAUDE.md when CLAUDECODE is set in
the environment (the caller is Claude Code) or when the directory has a
CLAUDE.md and no AGENTS.md; otherwise AGENTS.md.

The block sits between <!-- SALADCLOUD START --> and <!-- SALADCLOUD END -->
markers. Re-running replaces the block in place and leaves the rest of the
file untouched; a run that changes nothing exits 0 with action "unchanged".
A file with a broken pair of markers is left alone and reported as invalid
input (exit 6).

The block carries only stable facts about the CLI and the platform. Detailed
guidance lives in the SaladCloud skills, which the block tells the agent to
install (npx skills add https://docs.salad.com).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return app.runAgentInit(cmd, args, o) },
	}
	initCmd.Flags().StringVar(&o.file, "file", "", "file to write (default: AGENTS.md, or CLAUDE.md under Claude Code)")
	initCmd.Flags().StringVar(&o.dir, "dir", ".", "project directory")
	initCmd.Flags().BoolVar(&o.print, "print", false, "print the block to stdout instead of writing a file")

	agentCmd.AddCommand(initCmd)
	root.AddCommand(agentCmd)
}

// agentInitOptions are agent init's flags.
type agentInitOptions struct {
	file, dir string
	print     bool
}

func (app *App) runAgentInit(cmd *cobra.Command, _ []string, o agentInitOptions) error {
	file, dir, printOnly := o.file, o.dir, o.print

	if printOnly {
		res := agentInitResult{Action: "printed", OK: true, Reason: "--print", Version: version.String(), File: nil}
		return wrapErr(app.printer.Emit(res, func(w *output.Writer) {
			w.Print(agentdoc.Block(version.String()))
		}))
	}

	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, `--dir "%s" is not a directory`, dir)
	}

	target := agentdoc.PickFile(dir, file, app.Env.Getenv)
	action, cerr := updateAgentFile(target.Path)
	if cerr != nil {
		return cerr
	}

	shown := target.Path
	if rel, rerr := filepath.Rel(".", target.Path); rerr == nil && dir == "." {
		shown = rel
	}
	res := agentInitResult{File: &shown, Action: action, OK: true, Reason: target.Reason, Version: version.String()}
	return wrapErr(app.printer.Emit(res, func(w *output.Writer) {
		w.Print(agentInitLine(action, shown, target.Reason))
	}))
}

// updateAgentFile puts the current SaladCloud block into the file at path, creating it when there is none, and
// returns what that did: "created", "updated" or "unchanged" (nothing written).
func updateAgentFile(path string) (string, *clierr.Error) {
	existing, created, cerr := readAgentFile(path)
	if cerr != nil {
		return "", cerr
	}
	out, changed, cerr := applyAgentBlock(path, existing)
	if cerr != nil {
		return "", cerr
	}
	action := "unchanged"
	switch {
	case created:
		action = "created"
	case changed:
		action = "updated"
	}
	if action != "unchanged" {
		if cerr := writeAgentFile(path, out); cerr != nil {
			return "", cerr
		}
	}
	return action, nil
}

// readAgentFile reads the file agent init writes: its content, or "" and true when it does not exist yet.
func readAgentFile(path string) (string, bool, *clierr.Error) {
	existing, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", true, nil
	case err != nil:
		return "", false, clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot read %s: %v", path, err)
	}
	return string(existing), false, nil
}

// applyAgentBlock is the file's content with the current block in place, and whether that changed it; a file with
// a broken pair of markers is invalid input.
func applyAgentBlock(path, existing string) (string, bool, *clierr.Error) {
	out, changed, err := agentdoc.Apply(existing, version.String())
	if err != nil {
		if errors.Is(err, agentdoc.ErrMalformed) {
			reason := strings.TrimPrefix(err.Error(), agentdoc.ErrMalformed.Error()+": ")
			return "", false, clierr.New(
				clierr.CodeInvalidInput,
				clierr.KindInvalidInput,
				"%s: malformed SaladCloud block: %s",
				path,
				reason,
			)
		}
		return "", false, clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "%s: %v", path, err)
	}
	return out, changed, nil
}

// writeAgentFile writes out to path, creating its folder first.
func writeAgentFile(path, out string) *clierr.Error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot create the folder for %s: %v",
			path,
			err,
		)
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot write %s: %v", path, err)
	}
	return nil
}

// agentInitLine is agent init's table output: what it did to the file shown, and why that file.
func agentInitLine(action, shown, reason string) string {
	switch action {
	case "created":
		return fmt.Sprintf("Created %s with the SaladCloud block (%s)\n", shown, reason)
	case "updated":
		return fmt.Sprintf("Updated the SaladCloud block in %s (%s)\n", shown, reason)
	}
	return shown + " already has the current SaladCloud block; nothing to do\n"
}
