package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
)

// watchMinInterval is the smallest allowed --interval.
const watchMinInterval = 2 * time.Second

// addWatchFlags registers -w/--watch and --interval on a read command.
func addWatchFlags(cmd *cobra.Command) {
	cmd.Flags().
		BoolP("watch", "w", false, "poll and re-render the table until interrupted (Windows-friendly watch(1); table mode only)")
	cmd.Flags().Duration("interval", 5*time.Second, "poll interval for --watch (minimum 2s)")
	// Check the watch flags before the command builds its client, so a bad
	// combination is a usage error even with no API key set.
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if cerr := validateWatchFlags(cmd); cerr != nil {
			return cerr
		}
		return nil
	}
}

// validateWatchFlags rejects --watch outside table mode and an --interval
// below the minimum.
func validateWatchFlags(cmd *cobra.Command) *clierr.Error {
	if watch, _ := cmd.Flags().GetBool("watch"); !watch {
		return nil
	}
	if output.Mode() != "table" {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage,
			"--watch is table mode only; in scripts use --wait, or loop over '%s -o json'", cmd.CommandPath())
	}
	if interval, _ := cmd.Flags().GetDuration("interval"); interval < watchMinInterval {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--interval must be at least %s", watchMinInterval)
	}
	return nil
}

// runMaybeWatch runs render once, or — when --watch is set — in a loop until
// Ctrl-C (clean exit 0). --watch is for people watching a table: any other
// output mode is a usage error, because a stream of documents is not one
// document; scripts poll with --wait or a loop over 'get -o json'.
func runMaybeWatch(cmd *cobra.Command, render func(ctx context.Context) *clierr.Error) error {
	watch, _ := cmd.Flags().GetBool("watch")
	if !watch {
		if cerr := render(cmd.Context()); cerr != nil {
			return cerr
		}
		return nil
	}
	if cerr := validateWatchFlags(cmd); cerr != nil {
		return cerr
	}
	interval, _ := cmd.Flags().GetDuration("interval")
	return watchLoop(cmd.Context(), watchDeps{
		out:     output.Stdout,
		errOut:  os.Stderr,
		tty:     isTerminal(output.Stdout),
		now:     time.Now,
		after:   time.After,
		refresh: interval,
	}, render)
}

// watchDeps are watchLoop's dependencies, injectable for tests.
type watchDeps struct {
	out, errOut io.Writer
	tty         bool // stdout is a terminal: clear the screen between renders
	now         func() time.Time
	after       func(time.Duration) <-chan time.Time
	refresh     time.Duration
}

// watchLoop renders every d.refresh until ctx is cancelled (nil: Ctrl-C is
// the normal way to stop). On a terminal each render clears the screen, like
// watch(1), which Windows lacks; otherwise renders are separated by a blank
// line and no control codes are written. Transient errors are reported and
// polling continues; auth, usage and not-found errors end the watch.
func watchLoop(ctx context.Context, d watchDeps, render func(ctx context.Context) *clierr.Error) error {
	for i := 0; ; i++ {
		switch {
		case d.tty:
			// VT clear + home; supported by Windows Terminal, PowerShell,
			// and every Unix terminal.
			fmt.Fprint(d.out, "\x1b[2J\x1b[H")
		case i > 0:
			fmt.Fprintln(d.out)
		}
		fmt.Fprintf(d.out, "Every %s, at %s (Ctrl-C to stop)\n\n", d.refresh, d.now().Format(time.TimeOnly))
		if cerr := render(ctx); cerr != nil {
			if ctx.Err() != nil {
				return nil //nolint:nilerr // interrupted mid-request: --watch ends with exit 0
			}
			switch cerr.Code {
			case clierr.CodeAuth, clierr.CodeUsage, clierr.CodeNotFound:
				return cerr
			default:
				fmt.Fprintf(d.errOut, "watch: %s (retrying in %s)\n", cerr.Message, d.refresh)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-d.after(d.refresh):
		}
	}
}

// isTerminal reports whether w is a terminal.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
