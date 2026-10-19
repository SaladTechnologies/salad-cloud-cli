package cli

import (
	"errors"
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

func init() {
	agentCmd := &cobra.Command{
		Use:   "agent",
		Short: "Set up AI coding agents to work with SaladCloud",
		Long: `Commands that prepare a project or an agent to use SaladCloud through
this CLI. They never call the SaladCloud API and need no credentials.`,
	}

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
		RunE: runAgentInit,
	}
	initCmd.Flags().String("file", "", "file to write (default: AGENTS.md, or CLAUDE.md under Claude Code)")
	initCmd.Flags().String("dir", ".", "project directory")
	initCmd.Flags().Bool("print", false, "print the block to stdout instead of writing a file")

	agentCmd.AddCommand(initCmd)
	Register(agentCmd)
}

func runAgentInit(cmd *cobra.Command, _ []string) error {
	fr := readFlags(cmd.Flags())
	file := fr.String("file")
	dir := fr.String("dir")
	printOnly := fr.Bool("print")
	if cerr := fr.Err(); cerr != nil {
		return cerr
	}

	if printOnly {
		res := agentInitResult{Action: "printed", OK: true, Reason: "--print", Version: version.Version, File: nil}
		return wrapErr(printer.Emit(res, func(w *output.Writer) {
			w.Print(agentdoc.Block(version.Version))
		}))
	}

	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, `--dir "%s" is not a directory`, dir)
	}

	target := agentdoc.PickFile(dir, file, procEnv.Getenv)

	existing, err := os.ReadFile(target.Path)
	created := false
	switch {
	case errors.Is(err, os.ErrNotExist):
		created = true
	case err != nil:
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot read %s: %v", target.Path, err)
	}

	out, changed, err := agentdoc.Apply(string(existing), version.Version)
	if err != nil {
		if errors.Is(err, agentdoc.ErrMalformed) {
			reason := strings.TrimPrefix(err.Error(), agentdoc.ErrMalformed.Error()+": ")
			return clierr.New(
				clierr.CodeInvalidInput,
				clierr.KindInvalidInput,
				"%s: malformed SaladCloud block: %s",
				target.Path,
				reason,
			)
		}
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "%s: %v", target.Path, err)
	}

	action := "unchanged"
	if created {
		action = "created"
	} else if changed {
		action = "updated"
	}
	if created || changed {
		if err := os.MkdirAll(filepath.Dir(target.Path), 0o755); err != nil {
			return clierr.New(
				clierr.CodeGeneric,
				clierr.KindAPIError,
				"cannot create the folder for %s: %v",
				target.Path,
				err,
			)
		}
		if err := os.WriteFile(target.Path, []byte(out), 0o644); err != nil {
			return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot write %s: %v", target.Path, err)
		}
	}

	shown := target.Path
	if rel, rerr := filepath.Rel(".", target.Path); rerr == nil && dir == "." {
		shown = rel
	}
	res := agentInitResult{File: &shown, Action: action, OK: true, Reason: target.Reason, Version: version.Version}
	return wrapErr(printer.Emit(res, func(w *output.Writer) {
		switch action {
		case "created":
			w.Printf("Created %s with the SaladCloud block (%s)\n", shown, target.Reason)
		case "updated":
			w.Printf("Updated the SaladCloud block in %s (%s)\n", shown, target.Reason)
		default:
			w.Printf("%s already has the current SaladCloud block; nothing to do\n", shown)
		}
	}))
}
