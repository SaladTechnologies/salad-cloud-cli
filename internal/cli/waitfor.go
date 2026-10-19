// The --wait machinery shared by create/start/stop/update: poll a container
// group until it reaches a goal, with table-mode progress on stderr.
//
// Emission ordering contract: a command that waits emits its stdout payload
// only AFTER the wait succeeds. That keeps two promises at once — stdout is
// empty on any nonzero exit (timeout, interrupt), and json mode's single
// document describes the settled state rather than the pre-wait response.

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// Wait tuning.
const (
	waitPoll           = 5 * time.Second  // interval between polls (the first poll is immediate)
	waitDefaultTimeout = 10 * time.Minute // default --timeout for deploy-shaped waits (create/start/stop)

	// settleDefaultTimeout is the default verification window for write
	// verbs that settle by default (update, scale). A staged change was
	// observed to settle in 0-45 s, so this is sized for verification, not
	// for a deploy.
	settleDefaultTimeout = 2 * time.Minute
)

// goalSettle is the waitForGroup goal for "pending_change is false". It is
// named because its timeout is worded differently from the others: by the
// time it fires the write itself has already been accepted, and a message
// that reads like a failed write makes scripts retry a patch that landed.
const goalSettle = "settle"

// addWaitFlags registers --wait and --timeout on an action command; goal is
// the flag help's phrasing of what is waited for ("the group is running").
// They are per-command flags (not persistent): only create/start/stop/update
// wait.
func addWaitFlags(cmd *cobra.Command, goal string) {
	cmd.Flags().Bool("wait", false, fmt.Sprintf("block until %s (exit 1 on timeout)", goal))
	cmd.Flags().Duration("timeout", waitDefaultTimeout, "give up on --wait after this long")
}

// addSettleFlags registers --no-wait and --timeout on a write verb that
// settles by default (update, scale). Settling is the default because the
// intent diff — the CLI's only defense against the API accepting a write
// and silently discarding or mutating part of it — can only run against
// settled state, and whether a patch stages depends on what is in it (an
// image change does, an env change does not). Making the caller opt in
// made the warning's presence depend on the patch's contents, which is the
// failure that quietly teaches people to ignore it. See Settled decisions in AGENTS.md.
func addSettleFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("no-wait", false, "return once the API accepts the patch: no settle wait, no verification")
	cmd.Flags().
		Duration("timeout", settleDefaultTimeout, "give up verifying the change after this long (exit 1; the patch was already accepted)")
}

// settleFlagValues reads --no-wait/--timeout for a settle-by-default verb.
// wait is true unless --no-wait was given. Call it pre-flight, like
// waitFlagValues.
func settleFlagValues(cmd *cobra.Command) (bool, time.Duration, *clierr.Error) {
	fr := readFlags(cmd.Flags())
	noWait := fr.Bool("no-wait")
	timeout := fr.Duration("timeout")
	if cerr := fr.Err(); cerr != nil {
		return false, 0, cerr
	}
	if !noWait && timeout <= 0 {
		return false, 0, clierr.New(
			clierr.CodeUsage,
			clierr.KindUsage,
			"--timeout must be greater than zero (got %s)",
			timeout,
		)
	}
	return !noWait, timeout, nil
}

// waitFlagValues reads and validates --wait/--timeout. Call it pre-flight —
// before any file or API work — so a bad --timeout is a pure usage error.
func waitFlagValues(cmd *cobra.Command) (bool, time.Duration, *clierr.Error) {
	fr := readFlags(cmd.Flags())
	wait := fr.Bool("wait")
	timeout := fr.Duration("timeout")
	if cerr := fr.Err(); cerr != nil {
		return false, 0, cerr
	}
	if wait && timeout <= 0 {
		return false, 0, clierr.New(
			clierr.CodeUsage,
			clierr.KindUsage,
			"--timeout must be greater than zero (got %s)",
			timeout,
		)
	}
	return wait, timeout, nil
}

// waitDeps are waitForGroup's dependencies, injectable for tests.
type waitDeps struct {
	get   func(ctx context.Context) (*shared.ContainerGroup, *clierr.Error)
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error // returns ctx.Err() when canceled
	// progress receives the table-mode progress lines and transient-error
	// warnings; nil means os.Stderr.
	progress io.Writer
}

// progressRepeat is how often a wait repeats an unchanged progress line, so
// a long pending phase never looks hung.
const progressRepeat = time.Minute

// waitForGroup polls d.get until done(group) reports true, abort(group)
// returns an error, the context is canceled, or timeout elapses; on
// success it returns the group document that satisfied the goal (the
// settled state — this is what --wait callers emit). goal is the human
// phrasing for progress and timeout messages, e.g. `be "running"` or
// "settle". abort may be nil. The first poll is immediate, then one every
// poll interval.
//
// Transient API errors (network, 5xx, 429) warn on stderr and keep polling;
// not-found/auth/usage/invalid-input errors abort, because waiting longer
// cannot fix them. In table mode a progress line is written whenever the
// observed status or instance counts change, and repeated every
// progressRepeat while they do not; json mode prints no progress.
func waitForGroup(
	ctx context.Context,
	d waitDeps,
	group, goal string,
	done func(*shared.ContainerGroup) bool,
	abort func(*shared.ContainerGroup) *clierr.Error,
	timeout, poll time.Duration,
) (*shared.ContainerGroup, *clierr.Error) {
	stderr := d.progress
	if stderr == nil {
		stderr = os.Stderr
	}
	start := d.now()
	deadline := start.Add(timeout)
	lastStatus := "-"
	lastProgress := ""
	var lastPrinted time.Time
	for {
		g, cerr := d.get(ctx)
		switch {
		case cerr != nil && ctx.Err() != nil:
			return nil, waitInterrupted()
		case cerr != nil:
			switch cerr.Code {
			case clierr.CodeNotFound, clierr.CodeAuth, clierr.CodeUsage, clierr.CodeInvalidInput:
				return nil, cerr
			default:
				// Transient (network, 5xx, rate limit): note it, keep polling.
				fprintf(stderr, "warning: %s (still waiting)\n", cerr.Message)
			}
		default:
			status := groupStatus(g)
			lastStatus = status
			if done(g) {
				return g, nil
			}
			if abort != nil {
				if aerr := abort(g); aerr != nil {
					return nil, aerr
				}
			}
			if output.Mode() == "table" {
				// The elapsed suffix is excluded from the change check;
				// otherwise every poll would print.
				line := fmt.Sprintf("waiting for %s to %s: %s%s", group, goal, status, instanceCountSummary(g))
				if now := d.now(); line != lastProgress || now.Sub(lastPrinted) >= progressRepeat {
					lastProgress, lastPrinted = line, now
					fprintf(stderr, "%s (%s elapsed)\n", line, d.now().Sub(start).Round(time.Second))
				}
			}
		}
		if !d.now().Before(deadline) {
			if goal == goalSettle {
				// The write was accepted (HTTP 200) before this wait began;
				// only the verification ran out of time. Say so, or a
				// script will retry a patch that already landed.
				return nil, clierr.New(clierr.CodeGeneric, clierr.KindWaitTimeout,
					"patch accepted (HTTP 200), but %s is still pending after %s; not verified", group, timeout).
					WithHint("retry with a longer --timeout, or to check on the group, run saladcloud container-groups get %s", group)
			}
			return nil, clierr.New(clierr.CodeGeneric, clierr.KindWaitTimeout,
				"timed out after %s waiting for %s to %s (last status: %s)", timeout, group, goal, lastStatus).
				WithHint("retry with a longer --timeout, or to check on the group, run saladcloud container-groups get %s; the group continues server-side", group)
		}
		if err := d.sleep(ctx, poll); err != nil {
			return nil, waitInterrupted()
		}
	}
}

// waitForGroupStatus is waitForGroup with the goal "status == target",
// aborting early per abortUnreachable.
func waitForGroupStatus(
	ctx context.Context,
	d waitDeps,
	group, target string,
	fromActive bool,
	timeout, poll time.Duration,
) (*shared.ContainerGroup, *clierr.Error) {
	return waitForGroup(ctx, d, group, "be "+target,
		func(g *shared.ContainerGroup) bool { return groupStatus(g) == target },
		abortUnreachable(group, target, fromActive), timeout, poll)
}

// abortUnreachable returns the abort check for a wait whose target is
// "running": once the group has been seen pending or deploying, a stopped,
// failed or succeeded status means it will not get to running on its own,
// so the wait fails at once instead of polling out the whole --timeout.
// Before that, a stopped status is only the API not having acted yet — a
// start's first poll can still read stopped. fromActive says the caller
// already saw the group active (create: the response is pending with
// autostart on). Other targets get no abort (nil).
func abortUnreachable(group, target string, fromActive bool) func(*shared.ContainerGroup) *clierr.Error {
	if target != "running" {
		return nil
	}
	seenActive := fromActive
	return func(g *shared.ContainerGroup) *clierr.Error {
		switch status := groupStatus(g); status {
		case "pending", "deploying":
			seenActive = true
		case "stopped":
			if seenActive {
				return clierr.New(clierr.CodeGeneric, clierr.KindGroupStopped,
					"%s went to stopped before it was running; the platform stopped it or never started it", group).
					WithHint("to start it, run saladcloud container-groups start %s", group)
			}
		case "failed", "succeeded":
			if seenActive {
				return clierr.New(clierr.CodeGeneric, groupEndKind(status),
					"%s is %s, not running, and will not get there on its own", group, status).
					WithHint("to check it, run saladcloud container-groups get %s", group)
			}
		}
		return nil
	}
}

// groupEndKind is the error kind for a group that settled in a status the
// wait cannot get past.
func groupEndKind(status string) clierr.Kind {
	switch status {
	case "stopped":
		return clierr.KindGroupStopped
	case "failed":
		return clierr.KindGroupFailed
	case "succeeded":
		return clierr.KindGroupSucceeded
	}
	return clierr.KindAPIError
}

// waitInterrupted is the SIGINT/cancel result. Interrupting a wait is not
// success, and not silence either, because the server keeps going.
func waitInterrupted() *clierr.Error {
	return clierr.New(clierr.CodeGeneric, clierr.KindInterrupted, "wait interrupted; the group continues server-side")
}

// instanceCountSummary renders the nonzero instance counts compactly, e.g.
// " (instances: allocating 2, creating 1)"; "" when there is nothing to say.
func instanceCountSummary(g *shared.ContainerGroup) string {
	if g.CurrentState == nil || g.CurrentState.InstanceStatusCounts == nil {
		return ""
	}
	c := g.CurrentState.InstanceStatusCounts
	var parts []string
	add := func(name string, v *int64) {
		if n := ptr.Deref(v); n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", name, n))
		}
	}
	add("allocating", c.AllocatingCount)
	add("creating", c.CreatingCount)
	add("running", c.RunningCount)
	add("stopping", c.StoppingCount)
	if len(parts) == 0 {
		return ""
	}
	return " (instances: " + strings.Join(parts, ", ") + ")"
}

// liveWaitDeps builds the production waitDeps for one group.
func liveWaitDeps(client *api.Client, group string) waitDeps {
	return waitDeps{
		get: func(ctx context.Context) (*shared.ContainerGroup, *clierr.Error) {
			return client.GetGroup(ctx, group)
		},
		now: time.Now,
		sleep: func(ctx context.Context, dur time.Duration) error {
			t := time.NewTimer(dur)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}
}

// runGroupWait runs one --wait loop against the live API (Ctrl-C aware) and
// returns the group document that satisfied the goal. goal phrases the
// pursuit ("be \"running\""), doneMsg the arrival ("is running") for the
// table-mode confirmation on stderr ("" for none, when the caller's own
// stdout line says it); abort (may be nil) ends the wait early.
// The caller emits its stdout payload only after this returns nil error —
// see the package comment for why.
func runGroupWait(
	cmd *cobra.Command,
	client *api.Client,
	group, goal, doneMsg string,
	done func(*shared.ContainerGroup) bool,
	abort func(*shared.ContainerGroup) *clierr.Error,
	timeout time.Duration,
) (*shared.ContainerGroup, error) {
	ctx := cmd.Context() // canceled by the one signal handler in Execute
	start := time.Now()
	final, cerr := waitForGroup(ctx, liveWaitDeps(client, group), group, goal, done, abort, timeout, waitPoll)
	if cerr != nil {
		return nil, cerr
	}
	if output.Mode() == "table" && doneMsg != "" {
		fprintf(os.Stderr, "%s %s (waited %s)\n", group, doneMsg, time.Since(start).Round(time.Second))
	}
	return final, nil
}

// runStatusWait is runGroupWait for the common "status == target" goal,
// aborting early per abortUnreachable.
func runStatusWait(
	cmd *cobra.Command,
	client *api.Client,
	group, target string,
	fromActive bool,
	timeout time.Duration,
) (*shared.ContainerGroup, error) {
	final, err := runGroupWait(cmd, client, group, "be "+target, "is "+target,
		func(g *shared.ContainerGroup) bool { return groupStatus(g) == target },
		abortUnreachable(group, target, fromActive), timeout)
	if err != nil {
		return nil, err
	}
	if target == "running" && hasReadinessProbe(final) {
		// The group document counts running instances, not ready ones, so a
		// probe that never passes would otherwise look like success. One
		// instances call; if it fails, the note is skipped, not the wait.
		if col, cerr := client.ListInstances(cmd.Context(), group); cerr == nil {
			if note := readinessNote(group, col.Instances); note != "" {
				fprintln(os.Stderr, note)
			}
		}
	}
	return final, nil
}

// hasReadinessProbe reports whether the group defines a readiness probe.
// Without one an instance counts as ready once started, which can trail
// "running" by a moment, so only a probe makes not-ready worth a note.
func hasReadinessProbe(g *shared.ContainerGroup) bool {
	return g != nil && g.ReadinessProbe != nil && !g.ReadinessProbe.IsNull
}

// readinessNote is the stderr note for running instances whose readiness
// probe is not passing yet; "" when every running instance is ready.
func readinessNote(group string, instances []containergroups.ContainerGroupInstance) string {
	running, ready := 0, 0
	for _, in := range instances {
		if in.State == nil || *in.State != containergroups.THE_CONTAINER_GROUP_INSTANCE_STATE_RUNNING {
			continue
		}
		running++
		if ptr.Deref(in.Ready) {
			ready++
		}
	}
	if ready == running {
		return ""
	}
	return fmt.Sprintf(
		"note: %d of %d running instances ready (the readiness probe is not passing yet); to list them, run saladcloud container-groups instances list -c %s",
		ready,
		running,
		group,
	)
}
