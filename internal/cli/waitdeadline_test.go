package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/inferenceendpoints"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// The waits end at their --timeout: the last sleep is cut to the time left, so the last poll is at the deadline and
// never a whole poll interval past it (v0.2.0 returned success from update --wait-ready --timeout 11s after 20s).

func TestReadyWaitSucceedsAtTheDeadlineNotAfterIt(t *testing.T) {
	t.Parallel()
	running := shared.CONTAINER_GROUP_STATUS_RUNNING
	pulling := readyStep{
		status: running, replicas: 1,
		instances: []containergroups.ContainerGroupInstance{inst(1, stRunning, true)},
	}
	ready := readyStep{
		status: running, replicas: 1,
		instances: []containergroups.ContainerGroupInstance{inst(2, stRunning, true)},
	}
	start := time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC)
	g, cerr, h := runReady(t, newTestRun(t), []readyStep{pulling, pulling, ready}, 0, 11*time.Second)
	if cerr != nil || g == nil {
		t.Fatalf("err = %v, want the replicas ready at the deadline", cerr)
	}
	if took := h.now.Sub(start); took != 11*time.Second {
		t.Errorf("succeeded after %s, want at the 11s deadline (polls at 0s, 10s, 11s)", took)
	}
}

func TestReadyWaitTimesOutAtTheDeadline(t *testing.T) {
	t.Parallel()
	running := shared.CONTAINER_GROUP_STATUS_RUNNING
	pulling := readyStep{
		status: running, replicas: 1,
		instances: []containergroups.ContainerGroupInstance{inst(1, stRunning, true)},
	}
	for _, timeout := range []time.Duration{time.Second, 11 * time.Second, 25 * time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			t.Parallel()
			start := time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC)
			steps := []readyStep{pulling, pulling, pulling, pulling}
			_, cerr, h := runReady(t, newTestRun(t), steps, 0, timeout)
			if cerr == nil || cerr.Kind != clierr.KindWaitTimeout {
				t.Fatalf("err = %v, want wait_timeout", cerr)
			}
			if took := h.now.Sub(start); took != timeout {
				t.Errorf("timed out after %s, want exactly --timeout %s", took, timeout)
			}
			if strings.Contains(cerr.HintText(), "retry with a longer --timeout") {
				t.Errorf("hint still says to retry, which sends the update again: %q", cerr.HintText())
			}
		})
	}
}

func TestGroupWaitTimesOutAtTheDeadline(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	pending := statusStep(shared.CONTAINER_GROUP_STATUS_PENDING)
	cerr, h := app.runWait(t, []waitStep{pending, pending, pending}, 10, 7*time.Second)
	if cerr == nil || cerr.Kind != clierr.KindWaitTimeout {
		t.Fatalf("err = %v, want wait_timeout", cerr)
	}
	want := []time.Duration{5 * time.Second, 2 * time.Second}
	if len(h.sleeps) != len(want) || h.sleeps[0] != want[0] || h.sleeps[1] != want[1] {
		t.Errorf("sleeps = %v, want %v (the last one cut to the time left)", h.sleeps, want)
	}
}

func TestSettleWaitSharesTheDeadline(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	h := &waitHarness{
		t:     t,
		steps: []waitStep{{group: pendingGroup(), err: nil}, {group: pendingGroup(), err: nil}},
		now:   time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC), allowedSleeps: 10, cancel: func() {},
	}
	// The write took 8s of an 11s --timeout: the settle gets the 3s left, and still polls once at its start.
	deadline := h.now.Add(3 * time.Second)
	_, cerr := app.waitForGroupUntil(t.Context(), h.deps(), "demo", settleWaitGoal(), 11*time.Second, deadline,
		5*time.Second)
	if cerr == nil || cerr.Kind != clierr.KindWaitTimeout {
		t.Fatalf("err = %v, want wait_timeout", cerr)
	}
	if h.calls != 2 || len(h.sleeps) != 1 || h.sleeps[0] != 3*time.Second {
		t.Errorf("polls = %d, sleeps = %v, want 2 polls and one 3s sleep", h.calls, h.sleeps)
	}
	if !strings.Contains(cerr.Message, "--timeout 11s") {
		t.Errorf("message %q does not name the user's --timeout", cerr.Message)
	}
}

// pendingGroup is a group with a change still pending.
func pendingGroup() *shared.ContainerGroup {
	return &shared.ContainerGroup{Name: new("demo"), PendingChange: new(true)}
}

func TestJobWaitTimesOutAtTheDeadline(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	var progress bytes.Buffer
	d := scriptedJobWaitDeps([]inferenceendpoints.Status{inferenceendpoints.STATUS_RUNNING}, &progress)
	var slept []time.Duration
	sleep := d.sleep
	d.sleep = func(ctx context.Context, dur time.Duration) error {
		slept = append(slept, dur)
		return sleep(ctx, dur)
	}
	_, cerr := waitForJobTerminal(t.Context(), app.App, d, "job-1", 7*time.Second, 5*time.Second)
	if cerr == nil || cerr.Kind != clierr.KindWaitTimeout {
		t.Fatalf("err = %v, want wait_timeout", cerr)
	}
	var total time.Duration
	for _, s := range slept {
		total += s
	}
	if total != 7*time.Second {
		t.Errorf("slept %v (%s in all), want 7s in all", slept, total)
	}
	if strings.Contains(cerr.HintText(), "retry") {
		t.Errorf("hint says to retry, which makes a second job: %q", cerr.HintText())
	}
}

// TestPollCutShortByItsDeadline: a read the poll's own deadline ended is the wait's timeout, with no warning that
// it is still waiting; Ctrl-C (the wait's context) is still interrupted.
func TestPollCutShortByItsDeadline(t *testing.T) {
	t.Parallel()
	netErr := clierr.New(clierr.CodeNetwork, clierr.KindNetwork, "network error: context deadline exceeded")
	expired, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	t.Cleanup(cancel)
	<-expired.Done()
	var w strings.Builder
	if got := readyPollFailed(t.Context(), expired, &w, netErr); got != netErr || w.Len() != 0 {
		t.Errorf("deadline: got %v, warning %q; want the read's error and no warning", got, w.String())
	}
	parent, stop := context.WithCancel(t.Context())
	stop()
	if got := readyPollFailed(parent, parent, &w, netErr); got == nil || got.Kind != clierr.KindInterrupted {
		t.Errorf("Ctrl-C: got %v, want interrupted", got)
	}
	if !pollExpired(t.Context(), expired) || pollExpired(parent, parent) {
		t.Error("pollExpired must be true for the poll's own deadline only")
	}
}
