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
	"strings"
	"time"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
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
func addWaitFlags(cmd *cobra.Command, goal string, o *waitOptions) {
	cmd.Flags().BoolVar(&o.wait, "wait", false, fmt.Sprintf("block until %s (exit 1 on timeout)", goal))
	cmd.Flags().DurationVar(&o.timeout, "timeout", waitDefaultTimeout, "give up on --wait after this long")
}

// waitOptions are a command's --wait and --timeout (addWaitFlags, addJobWaitFlags).
type waitOptions struct {
	wait    bool
	timeout time.Duration
}

// settleOptions are a settle-by-default verb's --no-wait and --timeout
// (addSettleFlags).
type settleOptions struct {
	noWait  bool
	timeout time.Duration
}

// addSettleFlags registers --no-wait and --timeout on a write verb that
// settles by default (update, scale). Settling is the default because the
// intent diff — the CLI's only defense against the API accepting a write
// and silently discarding or mutating part of it — can only run against
// settled state, and whether a patch stages depends on what is in it (an
// image change does, an env change does not). Making the caller opt in
// made the warning's presence depend on the patch's contents, which is the
// failure that quietly teaches people to ignore it. See Settled decisions in AGENTS.md.
func addSettleFlags(cmd *cobra.Command, o *settleOptions) {
	cmd.Flags().
		BoolVar(&o.noWait, "no-wait", false, "return once the API accepts the patch: no settle wait, no verification")
	cmd.Flags().
		DurationVar(&o.timeout, "timeout", settleDefaultTimeout,
			"give up verifying the change after this long (exit 1; the patch was already accepted)")
}

// values checks --no-wait/--timeout for a settle-by-default verb. wait is
// true unless --no-wait was given. Call it pre-flight, like waitOptions'.
func (o settleOptions) values() (bool, time.Duration, *clierr.Error) {
	noWait, timeout := o.noWait, o.timeout
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

// values validates --wait/--timeout. Call it pre-flight — before any file or
// API work — so a bad --timeout is a pure usage error.
func (o waitOptions) values() (bool, time.Duration, *clierr.Error) {
	wait, timeout := o.wait, o.timeout
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
	// warnings; nil means errw().
	progress io.Writer
}

// progressRepeat is how often a wait repeats an unchanged progress line, so
// a long pending phase never looks hung.
const progressRepeat = time.Minute

// groupWaitGoal is what a wait pursues: goal is the human phrasing for
// progress and timeout messages, e.g. `be "running"` or "settle"; done
// reports that a group document reached it; abort, which may be nil, ends
// the wait early with an error.
type groupWaitGoal struct {
	goal  string
	done  func(*shared.ContainerGroup) bool
	abort func(*shared.ContainerGroup) *clierr.Error
}

// statusWaitGoal is the goal "status == target", aborting early per
// abortUnreachable.
func statusWaitGoal(group, target string, fromActive bool) groupWaitGoal {
	return groupWaitGoal{
		goal:  "be " + target,
		done:  func(g *shared.ContainerGroup) bool { return groupStatus(g) == target },
		abort: abortUnreachable(group, target, fromActive),
	}
}

// settleWaitGoal is the goal goalSettle: pending_change is false.
func settleWaitGoal() groupWaitGoal {
	return groupWaitGoal{
		goal:  goalSettle,
		done:  func(g *shared.ContainerGroup) bool { return !ptr.Deref(g.PendingChange) },
		abort: nil,
	}
}

// waitForGroup polls d.get until target.done(group) reports true,
// target.abort(group) returns an error, the context is canceled, or timeout
// elapses; on success it returns the group document that satisfied the goal
// (the settled state — this is what --wait callers emit). The first poll is
// immediate, then one every poll interval.
//
// Transient API errors (network, 5xx, 429) warn on stderr and keep polling;
// not-found/auth/usage/invalid-input errors abort, because waiting longer
// cannot fix them. In table mode a progress line is written whenever the
// observed status or instance counts change, and repeated every
// progressRepeat while they do not; json mode prints no progress.
func (app *App) waitForGroup(
	ctx context.Context,
	d waitDeps,
	group string,
	target groupWaitGoal,
	timeout, poll time.Duration,
) (*shared.ContainerGroup, *clierr.Error) {
	stderr := d.progress
	if stderr == nil {
		stderr = app.errw()
	}
	s := groupWaitState{
		w:            stderr,
		now:          d.now,
		group:        group,
		goal:         target.goal,
		start:        d.now(),
		lastStatus:   "-",
		lastProgress: "",
		lastPrinted:  time.Time{},
	}
	deadline := s.start.Add(timeout)
	for {
		g, cerr := d.get(ctx)
		final, ferr := app.waitObserve(ctx, &s, g, cerr, target)
		if final != nil || ferr != nil {
			return final, ferr
		}
		if !d.now().Before(deadline) {
			return nil, waitTimedOut(group, target.goal, timeout, s.lastStatus)
		}
		if err := d.sleep(ctx, poll); err != nil {
			return nil, waitInterrupted()
		}
	}
}

// groupWaitState is one waitForGroup loop: where its progress goes, its
// clock, what it waits for, and what it last saw and printed.
type groupWaitState struct {
	w            io.Writer
	now          func() time.Time
	group, goal  string
	start        time.Time
	lastStatus   string // "-" until a poll succeeds
	lastProgress string // the last progress line printed, without its elapsed suffix
	lastPrinted  time.Time
}

// waitObserve handles one poll: g and cerr are what it returned. It returns
// the group once target.done(g) holds, an error when the wait must end, and
// neither to keep polling (after a transient error, or while the goal is
// not reached yet).
func (app *App) waitObserve(
	ctx context.Context,
	s *groupWaitState,
	g *shared.ContainerGroup,
	cerr *clierr.Error,
	target groupWaitGoal,
) (*shared.ContainerGroup, *clierr.Error) {
	switch {
	case cerr != nil && ctx.Err() != nil:
		return nil, waitInterrupted()
	case cerr != nil:
		return nil, waitPollFailed(s.w, cerr)
	}
	s.lastStatus = groupStatus(g)
	if target.done(g) {
		return g, nil
	}
	if target.abort != nil {
		if aerr := target.abort(g); aerr != nil {
			return nil, aerr
		}
	}
	if !app.printer.Machine() {
		waitProgressLine(s, g)
	}
	return nil, nil
}

// waitPollFailed is the wait's answer to a failed poll: not-found, auth,
// usage and invalid-input errors end it, because waiting longer cannot fix
// them; anything else is transient (network, 5xx, rate limit), noted on w,
// and nil, to keep polling.
func waitPollFailed(w io.Writer, cerr *clierr.Error) *clierr.Error {
	switch cerr.Code {
	case clierr.CodeNotFound, clierr.CodeAuth, clierr.CodeUsage, clierr.CodeInvalidInput:
		return cerr
	default:
		// Transient (network, 5xx, rate limit): note it, keep polling.
		fprintf(w, "warning: %s (still waiting)\n", cerr.Message)
		return nil
	}
}

// waitProgressLine writes the table-mode progress line for g when the
// status or instance counts changed, or progressRepeat has passed since the
// last one.
func waitProgressLine(s *groupWaitState, g *shared.ContainerGroup) {
	// The elapsed suffix is excluded from the change check;
	// otherwise every poll would print.
	line := fmt.Sprintf("waiting for %s to %s: %s%s", s.group, s.goal, s.lastStatus, instanceCountSummary(g))
	if now := s.now(); line != s.lastProgress || now.Sub(s.lastPrinted) >= progressRepeat {
		s.lastProgress, s.lastPrinted = line, now
		fprintf(s.w, "%s (%s elapsed)\n", line, s.now().Sub(s.start).Round(time.Second))
	}
}

// waitTimedOut is the error for a wait that ran out of time.
func waitTimedOut(group, goal string, timeout time.Duration, lastStatus string) *clierr.Error {
	if goal == goalSettle {
		// The write was accepted (HTTP 200) before this wait began;
		// only the verification ran out of time. Say so, or a
		// script will retry a patch that already landed.
		return clierr.New(clierr.CodeGeneric, clierr.KindWaitTimeout,
			"patch accepted (HTTP 200), but %s is still pending after %s; not verified", group, timeout).
			WithHint("retry with a longer --timeout, or to check on the group, "+
				"run saladcloud container-groups get %s", group)
	}
	return clierr.New(clierr.CodeGeneric, clierr.KindWaitTimeout,
		"timed out after %s waiting for %s to %s (last status: %s)", timeout, group, goal, lastStatus).
		WithHint("retry with a longer --timeout, or to check on the group, "+
			"run saladcloud container-groups get %s; the group continues server-side", group)
}

// waitForGroupStatus is waitForGroup with the goal "status == target",
// aborting early per abortUnreachable.
func (app *App) waitForGroupStatus(
	ctx context.Context,
	d waitDeps,
	group, target string,
	fromActive bool,
	timeout, poll time.Duration,
) (*shared.ContainerGroup, *clierr.Error) {
	return app.waitForGroup(ctx, d, group, statusWaitGoal(group, target, fromActive), timeout, poll)
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
	if target != string(shared.CONTAINER_GROUP_STATUS_RUNNING) {
		return nil
	}
	seenActive := fromActive
	return func(g *shared.ContainerGroup) *clierr.Error {
		switch status := shared.ContainerGroupStatus(groupStatus(g)); status {
		case shared.CONTAINER_GROUP_STATUS_PENDING, shared.CONTAINER_GROUP_STATUS_DEPLOYING:
			seenActive = true
		case shared.CONTAINER_GROUP_STATUS_RUNNING:
			// The goal itself: done ends the wait before abort runs.
		case shared.CONTAINER_GROUP_STATUS_STOPPED:
			if seenActive {
				return clierr.New(clierr.CodeGeneric, clierr.KindGroupStopped,
					"%s went to stopped before it was running; the platform stopped it or never started it", group).
					WithHint("to start it, run saladcloud container-groups start %s", group)
			}
		case shared.CONTAINER_GROUP_STATUS_FAILED, shared.CONTAINER_GROUP_STATUS_SUCCEEDED:
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
func groupEndKind(status shared.ContainerGroupStatus) clierr.Kind {
	switch status {
	case shared.CONTAINER_GROUP_STATUS_STOPPED:
		return clierr.KindGroupStopped
	case shared.CONTAINER_GROUP_STATUS_FAILED:
		return clierr.KindGroupFailed
	case shared.CONTAINER_GROUP_STATUS_SUCCEEDED:
		return clierr.KindGroupSucceeded
	case shared.CONTAINER_GROUP_STATUS_PENDING, shared.CONTAINER_GROUP_STATUS_RUNNING,
		shared.CONTAINER_GROUP_STATUS_DEPLOYING:
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
		progress: nil,
	}
}

// runGroupWait runs one --wait loop for target against the live API (Ctrl-C
// aware) and returns the group document that satisfied the goal. doneMsg
// phrases the arrival ("is running") for the table-mode confirmation on
// stderr ("" for none, when the caller's own stdout line says it).
// The caller emits its stdout payload only after this returns nil error —
// see the package comment for why.
func (app *App) runGroupWait(
	cmd *cobra.Command,
	client *api.Client,
	group, doneMsg string,
	target groupWaitGoal,
	timeout time.Duration,
) (*shared.ContainerGroup, error) {
	ctx := cmd.Context() // canceled by the one signal handler in Execute
	start := time.Now()
	final, cerr := app.waitForGroup(ctx, liveWaitDeps(client, group), group, target, timeout, waitPoll)
	if cerr != nil {
		return nil, cerr
	}
	if !app.printer.Machine() && doneMsg != "" {
		fprintf(app.errw(), "%s %s (waited %s)\n", group, doneMsg, time.Since(start).Round(time.Second))
	}
	return final, nil
}

// runStatusWait is runGroupWait for the common "status == target" goal,
// aborting early per abortUnreachable.
func (app *App) runStatusWait(
	cmd *cobra.Command,
	client *api.Client,
	group, target string,
	fromActive bool,
	timeout time.Duration,
) (*shared.ContainerGroup, error) {
	final, err := app.runGroupWait(cmd, client, group, "is "+target, statusWaitGoal(group, target, fromActive), timeout)
	if err != nil {
		return nil, err
	}
	if target == string(shared.CONTAINER_GROUP_STATUS_RUNNING) && hasReadinessProbe(final) {
		// The group document counts running instances, not ready ones, so a
		// probe that never passes would otherwise look like success. One
		// instances call; if it fails, the note is skipped, not the wait.
		if col, cerr := client.ListInstances(cmd.Context(), group); cerr == nil {
			if note := readinessNote(group, col.Instances); note != "" {
				fprintln(app.errw(), note)
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
	for i := range instances {
		in := &instances[i]
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
		"note: %d of %d running instances ready (the readiness probe is not passing yet); "+
			"to list them, run saladcloud container-groups instances list -c %s",
		ready,
		running,
		group,
	)
}
