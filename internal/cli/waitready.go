// update --wait-ready: after an update settles, wait until the group's replicas run its new version and are ready,
// optionally for a while. Settling only means the platform took the change; the instances then restart onto the new
// version as their nodes check in, pulling the image again, which for a large image takes many minutes.

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

// Ready-wait tuning.
const (
	// readyPoll is the ready wait's poll interval: each poll reads the group and its instances, and a rollout takes
	// minutes, so it polls less often than a status wait.
	readyPoll = 10 * time.Second
	// readyDefaultTimeout is update's --timeout under --wait-ready when none is given: a large image's pull and an
	// app's warm-up can take most of an hour.
	readyDefaultTimeout = time.Hour
)

// readyOptions are update's --wait-ready and --ready-for.
type readyOptions struct {
	wait     bool
	readyFor time.Duration
}

// addReadyFlags registers --wait-ready and --ready-for on update.
func addReadyFlags(cmd *cobra.Command, o *readyOptions) {
	cmd.Flags().BoolVar(&o.wait, "wait-ready", false,
		"after the change settles, wait until every replica runs the group's new version and is ready "+
			"(exit 1 on timeout; --timeout then defaults to 1h)")
	cmd.Flags().DurationVar(&o.readyFor, "ready-for", 0,
		"with --wait-ready, how long the replicas must stay ready before the command returns (0: ready once)")
}

// check validates --wait-ready and --ready-for against each other and --no-wait. Call it pre-flight, like the other
// wait options.
func (o readyOptions) check(noWait bool) *clierr.Error {
	switch {
	case o.readyFor < 0:
		return clierr.New(clierr.CodeUsage, clierr.KindUsage,
			"--ready-for must be zero or more (got %s)", o.readyFor)
	case o.readyFor > 0 && !o.wait:
		return clierr.New(clierr.CodeUsage, clierr.KindUsage,
			"--ready-for requires --wait-ready")
	case o.wait && noWait:
		return clierr.New(clierr.CodeUsage, clierr.KindUsage,
			"--wait-ready and --no-wait cannot be combined").
			WithHint("--wait-ready waits for the settle first; drop --no-wait")
	}
	return nil
}

// readyDeps are waitForReady's dependencies, injectable for tests.
type readyDeps struct {
	group     func(ctx context.Context) (*shared.ContainerGroup, *clierr.Error)
	instances func(ctx context.Context) ([]containergroups.ContainerGroupInstance, *clierr.Error)
	now       func() time.Time
	sleep     func(ctx context.Context, d time.Duration) error // returns ctx.Err() when canceled
	// progress receives the table-mode progress lines and transient-error warnings; nil means errw().
	progress io.Writer
}

// readyGoal is what a ready wait waits for: the group's replicas on version (or a later one), running and ready, for
// readyFor. timeout is the command's --timeout, which the message names when the deadline passes.
type readyGoal struct {
	group    string
	version  int64
	readyFor time.Duration
	timeout  time.Duration
}

// readyWait is one waitForReady loop: what it waits for, and what it last saw and printed.
type readyWait struct {
	readyGoal

	w            io.Writer
	now          func() time.Time
	start        time.Time
	readySince   time.Time // when the replicas were last seen all ready; zero while they are not
	last         string    // the last progress state, for the timeout message
	lastProgress string
	lastPrinted  time.Time
}

// waitForReady polls until goal holds and returns the group document from that poll, or fails once deadline passes.
// A group that stopped, failed or finished cannot get there and ends the wait at once; transient API errors warn and
// keep polling, as in waitForGroup.
func (app *App) waitForReady(
	ctx context.Context,
	d readyDeps,
	goal readyGoal,
	deadline time.Time,
	poll time.Duration,
) (*shared.ContainerGroup, *clierr.Error) {
	w := d.progress
	if w == nil {
		w = app.errw()
	}
	s := readyWait{
		readyGoal: goal, w: w, now: d.now, start: d.now(),
		readySince: time.Time{}, last: "no reading yet", lastProgress: "", lastPrinted: time.Time{},
	}
	for {
		g, done, cerr := app.readyPoll(ctx, d, &s)
		if done || cerr != nil {
			return g, cerr
		}
		if !d.now().Before(deadline) {
			return nil, readyTimedOut(&s)
		}
		if err := d.sleep(ctx, poll); err != nil {
			return nil, waitInterrupted()
		}
	}
}

// readyPoll is one poll of waitForReady: it reads the group and its instances and reports whether the wait is done,
// with the group document when it is.
func (app *App) readyPoll(
	ctx context.Context,
	d readyDeps,
	s *readyWait,
) (*shared.ContainerGroup, bool, *clierr.Error) {
	g, cerr := d.group(ctx)
	if cerr != nil {
		return nil, false, readyPollFailed(ctx, s.w, cerr)
	}
	switch status := shared.ContainerGroupStatus(groupStatus(g)); status {
	case shared.CONTAINER_GROUP_STATUS_STOPPED, shared.CONTAINER_GROUP_STATUS_FAILED,
		shared.CONTAINER_GROUP_STATUS_SUCCEEDED:
		return nil, false, clierr.New(clierr.CodeGeneric, groupEndKind(status),
			"%s is %s, so its replicas will not run version %d", s.group, status, s.version).
			WithHint("to check it, run saladcloud container-groups get %s", s.group)
	case shared.CONTAINER_GROUP_STATUS_PENDING, shared.CONTAINER_GROUP_STATUS_RUNNING,
		shared.CONTAINER_GROUP_STATUS_DEPLOYING:
	}
	want := ptr.Deref(g.Replicas)
	if want <= 0 {
		return g, true, nil // nothing to wait for
	}
	instances, cerr := d.instances(ctx)
	if cerr != nil {
		return nil, false, readyPollFailed(ctx, s.w, cerr)
	}
	ready := readyOnVersion(instances, s.version)
	s.last = fmt.Sprintf("%d of %d replicas ready on version %d%s", ready, want, s.version,
		instanceVersionSummary(instances, s.version))
	now := s.now()
	if int64(ready) < want {
		s.readySince = time.Time{}
		app.readyProgress(s, s.last)
		return nil, false, nil
	}
	if s.readySince.IsZero() {
		s.readySince = now
	}
	held := now.Sub(s.readySince)
	if held >= s.readyFor {
		return g, true, nil
	}
	app.readyProgress(s, fmt.Sprintf("%s, staying ready for %s (%s so far)", s.last, s.readyFor,
		held.Round(time.Second)))
	return nil, false, nil
}

// readyPollFailed is the ready wait's answer to a failed read: Ctrl-C ends it as interrupted, and otherwise it is
// waitPollFailed's (an error that waiting cannot fix ends the wait; a transient one warns and keeps polling).
func readyPollFailed(ctx context.Context, w io.Writer, cerr *clierr.Error) *clierr.Error {
	if ctx.Err() != nil {
		return waitInterrupted()
	}
	return waitPollFailed(w, cerr)
}

// readyProgress writes the table-mode progress line when it changed, or once a minute while it does not.
func (app *App) readyProgress(s *readyWait, line string) {
	if app.printer.Machine() {
		return
	}
	line = fmt.Sprintf("waiting for %s: %s", s.group, line)
	if now := s.now(); line != s.lastProgress || now.Sub(s.lastPrinted) >= progressRepeat {
		s.lastProgress, s.lastPrinted = line, now
		fprintf(s.w, "%s (%s elapsed)\n", line, now.Sub(s.start).Round(time.Second))
	}
}

// readyTimedOut is the error for a ready wait that ran out of time. The update itself was applied, so the message
// says so: a script must not retry it.
func readyTimedOut(s *readyWait) *clierr.Error {
	return clierr.New(clierr.CodeGeneric, clierr.KindWaitTimeout,
		"update applied, but %s was not ready within --timeout %s: %s", s.group, s.timeout, s.last).
		WithHint("to see its instances, run saladcloud container-groups instances list -c %s; "+
			"the rollout continues server-side, so retry with a longer --timeout while it is still pulling", s.group)
}

// readyOnVersion counts the instances that run version or a later one, are running, and are ready. An instance
// without a readiness probe is ready once it has started.
func readyOnVersion(instances []containergroups.ContainerGroupInstance, version int64) int {
	n := 0
	for i := range instances {
		in := &instances[i]
		if ptr.Deref(in.Version) >= version && in.State != nil &&
			*in.State == containergroups.THE_CONTAINER_GROUP_INSTANCE_STATE_RUNNING && ptr.Deref(in.Ready) {
			n++
		}
	}
	return n
}

// instanceVersionSummary renders where the instances are, e.g. " (instances: downloading 1, old version running 1)";
// "" when there are none.
func instanceVersionSummary(instances []containergroups.ContainerGroupInstance, version int64) string {
	counts := make(map[string]int)
	var order []string
	for i := range instances {
		in := &instances[i]
		key := instanceState(in)
		if ptr.Deref(in.Version) < version {
			key = "old version " + key
		} else if key == string(containergroups.THE_CONTAINER_GROUP_INSTANCE_STATE_RUNNING) && !ptr.Deref(in.Ready) {
			key = "running, not ready"
		}
		if counts[key] == 0 {
			order = append(order, key)
		}
		counts[key]++
	}
	if len(order) == 0 {
		return ""
	}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
	}
	return " (instances: " + strings.Join(parts, ", ") + ")"
}

// liveReadyDeps builds the production readyDeps for one group.
func liveReadyDeps(client *api.Client, group string) readyDeps {
	live := liveWaitDeps(client, group)
	return readyDeps{
		group: live.get,
		instances: func(ctx context.Context) ([]containergroups.ContainerGroupInstance, *clierr.Error) {
			col, cerr := client.ListInstances(ctx, group)
			if cerr != nil {
				return nil, cerr
			}
			return col.Instances, nil
		},
		now:      live.now,
		sleep:    live.sleep,
		progress: nil,
	}
}

// runReadyWait runs update's --wait-ready against the live API (Ctrl-C aware) for the settled group, until deadline,
// and returns the group document from the poll that found its replicas ready. The table-mode line on stderr says how
// long it took.
func (app *App) runReadyWait(
	cmd *cobra.Command,
	client *api.Client,
	name string,
	settled *shared.ContainerGroup,
	o readyOptions,
	timeout time.Duration,
	deadline time.Time,
) (*shared.ContainerGroup, *clierr.Error) {
	version := ptr.Deref(settled.Version)
	if version <= 0 {
		return nil, clierr.New(clierr.CodeGeneric, clierr.KindAPIError,
			"the API returned %s without its version, so --wait-ready cannot tell new instances from old", name)
	}
	start := time.Now()
	goal := readyGoal{group: name, version: version, readyFor: o.readyFor, timeout: timeout}
	final, cerr := app.waitForReady(cmd.Context(), liveReadyDeps(client, name), goal, deadline, readyPoll)
	if cerr != nil {
		return nil, cerr
	}
	if !app.printer.Machine() {
		fprintf(app.errw(), "%s is ready on version %d (waited %s)\n", name, version,
			time.Since(start).Round(time.Second))
	}
	return final, nil
}
