package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/util"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// waitStep is one scripted GetGroup response.
type waitStep struct {
	group *shared.ContainerGroup
	err   *clierr.Error
}

// statusStep scripts a poll observing the given status (no instance counts).
func statusStep(status shared.ContainerGroupStatus) waitStep {
	return waitStep{group: &shared.ContainerGroup{
		Name:         ptr.To("demo"),
		CurrentState: &shared.ContainerGroupState{Status: &status},
	}}
}

// waitHarness scripts get responses and drives a fake clock: each sleep
// advances now by the requested duration; after allowedSleeps the context is
// canceled, the way a SIGINT would.
type waitHarness struct {
	t             *testing.T
	steps         []waitStep
	calls         int
	now           time.Time
	sleeps        []time.Duration
	allowedSleeps int
	cancel        context.CancelFunc
	stderr        strings.Builder
	final         *shared.ContainerGroup // waitForGroupStatus's success return
}

func (h *waitHarness) deps() waitDeps {
	return waitDeps{
		get: func(_ context.Context) (*shared.ContainerGroup, *clierr.Error) {
			if h.calls >= len(h.steps) {
				h.t.Fatalf("get called %d times, only %d responses scripted", h.calls+1, len(h.steps))
			}
			s := h.steps[h.calls]
			h.calls++
			return s.group, s.err
		},
		now: func() time.Time { return h.now },
		sleep: func(ctx context.Context, d time.Duration) error {
			h.sleeps = append(h.sleeps, d)
			if len(h.sleeps) > h.allowedSleeps {
				h.cancel()
				return ctx.Err()
			}
			h.now = h.now.Add(d)
			return nil
		},
		progress: &h.stderr,
	}
}

// runWait drives waitForGroupStatus against a scripted harness, waiting for
// "running" the way start --wait does (no prior sighting of the group).
func (app *testRun) runWait(
	t *testing.T,
	steps []waitStep,
	allowedSleeps int,
	timeout time.Duration,
) (*clierr.Error, *waitHarness) {
	t.Helper()
	return app.runWaitFrom(t, steps, false, allowedSleeps, timeout)
}

// runWaitFrom is runWait with fromActive set by the caller (true is
// create --wait: the create response was already pending).
func (app *testRun) runWaitFrom(
	t *testing.T,
	steps []waitStep,
	fromActive bool,
	allowedSleeps int,
	timeout time.Duration,
) (*clierr.Error, *waitHarness) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h := &waitHarness{
		t:             t,
		steps:         steps,
		now:           time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC),
		allowedSleeps: allowedSleeps,
		cancel:        cancel,
	}
	final, cerr := app.waitForGroupStatus(ctx, h.deps(), "demo", "running", fromActive, timeout, 5*time.Second)
	h.final = final
	return cerr, h
}

func TestWaitSuccessAfterPolls(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	cerr, h := app.runWait(t, []waitStep{
		statusStep(shared.CONTAINER_GROUP_STATUS_PENDING),
		statusStep(shared.CONTAINER_GROUP_STATUS_DEPLOYING),
		statusStep(shared.CONTAINER_GROUP_STATUS_RUNNING),
	}, 10, time.Hour)
	if cerr != nil {
		t.Fatalf("waitForGroupStatus returned %v, want nil", cerr)
	}
	if h.calls != 3 {
		t.Errorf("get called %d times, want 3", h.calls)
	}
	if len(h.sleeps) != 2 {
		t.Errorf("slept %d times, want 2 (the first poll is immediate)", len(h.sleeps))
	}
	// The success return is the group document that satisfied the goal —
	// what --wait callers emit as the settled state.
	if h.final == nil || groupStatus(h.final) != "running" {
		t.Errorf("final group = %+v, want the running poll's document", h.final)
	}
}

func TestWaitTimeoutNamesLastStatus(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	// timeout 7s, poll 5s: polls at 0s and 5s pass the deadline check, the
	// poll at 10s trips it.
	cerr, _ := app.runWait(t, []waitStep{
		statusStep(shared.CONTAINER_GROUP_STATUS_PENDING),
		statusStep(shared.CONTAINER_GROUP_STATUS_DEPLOYING),
		statusStep(shared.CONTAINER_GROUP_STATUS_DEPLOYING),
	}, 10, 7*time.Second)
	if cerr == nil {
		t.Fatal("waitForGroupStatus returned nil, want a timeout error")
	}
	if cerr.Code != clierr.CodeGeneric {
		t.Errorf("code = %d, want %d", cerr.Code, clierr.CodeGeneric)
	}
	if cerr.Kind != clierr.KindWaitTimeout {
		t.Errorf("kind = %q, want wait_timeout", cerr.Kind)
	}
	for _, want := range []string{"timed out after 7s", "demo", "last status: deploying", "--timeout"} {
		if !strings.Contains(errText(cerr), want) {
			t.Errorf("message %q does not contain %q", errText(cerr), want)
		}
	}
}

func TestWaitTransientErrorToleratedThenSuccess(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	cerr, h := app.runWait(t, []waitStep{
		{err: clierr.New(clierr.CodeNetwork, clierr.KindNetwork, "connection reset")},
		{err: clierr.New(clierr.CodeRateLimited, clierr.KindRateLimited, "429")},
		statusStep(shared.CONTAINER_GROUP_STATUS_RUNNING),
	}, 10, time.Hour)
	if cerr != nil {
		t.Fatalf("waitForGroupStatus returned %v, want nil (transient errors must not abort)", cerr)
	}
	if h.calls != 3 {
		t.Errorf("get called %d times, want 3", h.calls)
	}
	if got := strings.Count(h.stderr.String(), "warning:"); got != 2 {
		t.Errorf("printed %d warnings, want 2:\n%s", got, h.stderr.String())
	}
}

func TestWaitNotFoundAborts(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	nf := clierr.New(clierr.CodeNotFound, clierr.KindNotFound, `container group "demo" not found`)
	cerr, h := app.runWait(t, []waitStep{{err: nf}}, 10, time.Hour)
	if cerr != nf {
		t.Fatalf("waitForGroupStatus returned %v, want the 404 error itself", cerr)
	}
	if len(h.sleeps) != 0 {
		t.Errorf("slept %d times after a 404, want 0", len(h.sleeps))
	}
}

func TestWaitAuthAborts(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	auth := clierr.New(clierr.CodeAuth, clierr.KindAuth, "key revoked")
	cerr, _ := app.runWait(t, []waitStep{{err: auth}}, 10, time.Hour)
	if cerr != auth {
		t.Fatalf("waitForGroupStatus returned %v, want the auth error itself", cerr)
	}
}

func TestWaitCancelReturnsInterrupted(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	// allowedSleeps 1: the second sleep cancels, as Ctrl-C would.
	cerr, h := app.runWait(t, []waitStep{
		statusStep(shared.CONTAINER_GROUP_STATUS_PENDING),
		statusStep(shared.CONTAINER_GROUP_STATUS_PENDING),
	}, 1, time.Hour)
	if cerr == nil {
		t.Fatal("waitForGroupStatus returned nil on cancellation, want an interrupted error")
	}
	if cerr.Code != clierr.CodeGeneric {
		t.Errorf("code = %d, want %d (an interrupted wait is not success)", cerr.Code, clierr.CodeGeneric)
	}
	if want := "wait interrupted; the group continues server-side"; cerr.Message != want {
		t.Errorf("message = %q, want %q", cerr.Message, want)
	}
	if h.calls != 2 {
		t.Errorf("get called %d times, want 2", h.calls)
	}
}

func TestWaitProgressDedupsUnchangedLines(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	counts := &shared.ContainerGroupInstanceStatusCount{
		AllocatingCount: ptr.To(int64(2)),
		CreatingCount:   ptr.To(int64(1)),
		RunningCount:    ptr.To(int64(0)),
		StoppingCount:   ptr.To(int64(0)),
	}
	pending := shared.CONTAINER_GROUP_STATUS_PENDING
	withCounts := waitStep{group: &shared.ContainerGroup{
		Name:         ptr.To("demo"),
		CurrentState: &shared.ContainerGroupState{Status: &pending, InstanceStatusCounts: counts},
	}}
	cerr, h := app.runWait(t, []waitStep{
		withCounts, // progress line 1
		withCounts, // unchanged: no line
		withCounts, // unchanged: no line
		statusStep(shared.CONTAINER_GROUP_STATUS_DEPLOYING), // changed: line 2
		statusStep(shared.CONTAINER_GROUP_STATUS_RUNNING),   // success: no line
	}, 10, time.Hour)
	if cerr != nil {
		t.Fatalf("waitForGroupStatus returned %v, want nil", cerr)
	}
	got := h.stderr.String()
	if n := strings.Count(got, "waiting for"); n != 2 {
		t.Errorf("printed %d progress lines, want 2 (dedup on unchanged state):\n%s", n, got)
	}
	// Nonzero counts only, and the elapsed suffix present.
	if !strings.Contains(
		got,
		`waiting for demo to be running: pending (instances: allocating 2, creating 1) (0s elapsed)`,
	) {
		t.Errorf("first progress line missing or malformed:\n%s", got)
	}
	if strings.Contains(got, "running 0") || strings.Contains(got, "stopping 0") {
		t.Errorf("zero counts must be omitted:\n%s", got)
	}
}

func TestWaitProgressRepeatsEveryMinute(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	// Poll every 5s: 25 unchanged pending polls span 0s-120s, so the line
	// prints at 0s, 60s and 120s; the running poll at 125s ends the wait.
	steps := make([]waitStep, 0, 26)
	for range 25 {
		steps = append(steps, statusStep(shared.CONTAINER_GROUP_STATUS_PENDING))
	}
	steps = append(steps, statusStep(shared.CONTAINER_GROUP_STATUS_RUNNING))
	cerr, h := app.runWait(t, steps, 100, time.Hour)
	if cerr != nil {
		t.Fatalf("waitForGroupStatus returned %v, want nil", cerr)
	}
	got := h.stderr.String()
	if n := strings.Count(got, "waiting for"); n != 3 {
		t.Errorf("printed %d progress lines, want 3 (one a minute while unchanged):\n%s", n, got)
	}
	for _, want := range []string{"(0s elapsed)", "(1m0s elapsed)", "(2m0s elapsed)"} {
		if !strings.Contains(got, want) {
			t.Errorf("progress missing %q:\n%s", want, got)
		}
	}
}

func TestReadinessNote(t *testing.T) {
	t.Parallel()
	inst := func(state containergroups.TheContainerGroupInstanceState, ready bool) containergroups.ContainerGroupInstance {
		return containergroups.ContainerGroupInstance{State: &state, Ready: &ready}
	}
	running, creating := containergroups.THE_CONTAINER_GROUP_INSTANCE_STATE_RUNNING, containergroups.THE_CONTAINER_GROUP_INSTANCE_STATE_CREATING
	cases := []struct {
		name string
		in   []containergroups.ContainerGroupInstance
		want string
	}{
		{"all ready", []containergroups.ContainerGroupInstance{inst(running, true), inst(running, true)}, ""},
		{"none", nil, ""},
		{
			"not running ignored",
			[]containergroups.ContainerGroupInstance{inst(running, true), inst(creating, false)},
			"",
		},
		{
			"one not ready",
			[]containergroups.ContainerGroupInstance{inst(running, true), inst(running, false)},
			"note: 1 of 2 running instances ready",
		},
		{
			"ready unknown",
			[]containergroups.ContainerGroupInstance{{State: &running}},
			"note: 0 of 1 running instances ready",
		},
	}
	for _, c := range cases {
		got := readinessNote("demo", c.in)
		if c.want == "" && got != "" || c.want != "" && !strings.HasPrefix(got, c.want) {
			t.Errorf("%s: readinessNote = %q, want prefix %q", c.name, got, c.want)
		}
		if got != "" && !strings.Contains(got, "instances list -c demo") {
			t.Errorf("%s: note does not point at instances list: %q", c.name, got)
		}
	}
}

func TestHasReadinessProbe(t *testing.T) {
	t.Parallel()
	if hasReadinessProbe(&shared.ContainerGroup{}) {
		t.Error("no readiness_probe reported as having one")
	}
	g := &shared.ContainerGroup{}
	g.SetReadinessProbeNull()
	if hasReadinessProbe(g) {
		t.Error("readiness_probe null reported as having one")
	}
	g.SetReadinessProbe(
		util.Nullable[shared.ContainerGroupReadinessProbe]{Value: shared.ContainerGroupReadinessProbe{}},
	)
	if !hasReadinessProbe(g) {
		t.Error("a readiness_probe was not detected")
	}
}

func TestWaitJSONModePrintsNoProgress(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	if err := app.printer.SetMode("json"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.printer.SetMode("table"); err != nil {
			t.Fatal(err)
		}
	})
	cerr, h := app.runWait(t, []waitStep{
		statusStep(shared.CONTAINER_GROUP_STATUS_PENDING),
		statusStep(shared.CONTAINER_GROUP_STATUS_RUNNING),
	}, 10, time.Hour)
	if cerr != nil {
		t.Fatalf("waitForGroupStatus returned %v, want nil", cerr)
	}
	if h.stderr.Len() != 0 {
		t.Errorf("json mode printed progress:\n%s", h.stderr.String())
	}
}

// TestWaitForGroupSettlePredicate drives the generic waitForGroup with
// update --wait's goal: pending_change false.
func TestWaitForGroupSettlePredicate(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	running := shared.CONTAINER_GROUP_STATUS_RUNNING
	step := func(pending bool) waitStep {
		return waitStep{group: &shared.ContainerGroup{
			Name:          ptr.To("demo"),
			PendingChange: ptr.To(pending),
			CurrentState:  &shared.ContainerGroupState{Status: &running},
		}}
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	h := &waitHarness{
		t:             t,
		steps:         []waitStep{step(true), step(true), step(false)},
		now:           time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC),
		allowedSleeps: 10,
		cancel:        cancel,
	}
	final, cerr := app.waitForGroup(ctx, h.deps(), "demo", "settle",
		func(g *shared.ContainerGroup) bool { return !ptr.Deref(g.PendingChange) }, nil,
		time.Hour, 5*time.Second)
	if cerr != nil {
		t.Fatalf("waitForGroup returned %v, want nil", cerr)
	}
	if final == nil || ptr.Deref(final.PendingChange) {
		t.Errorf("final group = %+v, want the settled poll's document", final)
	}
	if !strings.Contains(h.stderr.String(), `waiting for demo to settle`) {
		t.Errorf("progress line missing the settle goal:\n%s", h.stderr.String())
	}
}

func TestInstanceCountSummaryEmpty(t *testing.T) {
	t.Parallel()
	if got := instanceCountSummary(&shared.ContainerGroup{}); got != "" {
		t.Errorf("instanceCountSummary(no state) = %q, want empty", got)
	}
	zero := &shared.ContainerGroup{CurrentState: &shared.ContainerGroupState{
		InstanceStatusCounts: &shared.ContainerGroupInstanceStatusCount{
			AllocatingCount: ptr.To(int64(0)),
		},
	}}
	if got := instanceCountSummary(zero); got != "" {
		t.Errorf("instanceCountSummary(all zero) = %q, want empty", got)
	}
}

// TestSettleTimeoutSaysPatchAccepted: when a settle wait times out, the
// write itself already succeeded, and the message must say so — a script
// that reads it as a failed write retries a patch that landed.
func TestSettleTimeoutSaysPatchAccepted(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	running := shared.CONTAINER_GROUP_STATUS_RUNNING
	pending := waitStep{group: &shared.ContainerGroup{
		Name:          ptr.To("demo"),
		PendingChange: ptr.To(true),
		CurrentState:  &shared.ContainerGroupState{Status: &running},
	}}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	h := &waitHarness{
		t:             t,
		steps:         []waitStep{pending, pending, pending},
		now:           time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC),
		allowedSleeps: 10,
		cancel:        cancel,
	}
	_, cerr := app.waitForGroup(ctx, h.deps(), "demo", goalSettle,
		func(g *shared.ContainerGroup) bool { return !ptr.Deref(g.PendingChange) }, nil,
		10*time.Second, 5*time.Second)
	if cerr == nil {
		t.Fatal("waitForGroup returned nil, want wait_timeout")
	}
	if cerr.Kind != clierr.KindWaitTimeout || cerr.Code != clierr.CodeGeneric {
		t.Errorf("error = kind %q code %d, want wait_timeout / generic", cerr.Kind, cerr.Code)
	}
	for _, want := range []string{"patch accepted", "still pending", "not verified", "saladcloud container-groups " +
		"get demo"} {
		if !strings.Contains(errText(cerr), want) {
			t.Errorf("settle timeout message %q lacks %q", errText(cerr), want)
		}
	}
	if strings.Contains(cerr.Message, "timed out after") {
		t.Errorf("settle timeout message must not read like a failed write: %q", cerr.Message)
	}
}

// TestSettleFlags pins the settle-by-default flag contract: --no-wait
// exists, --wait does not, the default timeout is the short verification
// window, and a non-positive timeout is a usage error only when waiting.
func TestSettleFlags(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{Use: "x"}
	addSettleFlags(cmd, &settleOptions{})
	if cmd.Flags().Lookup("wait") != nil {
		t.Error("a settle verb must not carry --wait: waiting is the default")
	}
	wait, timeout, cerr := settleValuesWith(t)
	if cerr != nil || !wait || timeout != settleDefaultTimeout {
		t.Errorf(
			"defaults = wait %v timeout %s err %v, want wait true, %s, nil",
			wait,
			timeout,
			cerr,
			settleDefaultTimeout,
		)
	}
	if _, _, cerr = settleValuesWith(t, "timeout", "0"); cerr == nil || cerr.Code != clierr.CodeUsage {
		t.Errorf("zero timeout while waiting = %v, want a usage error", cerr)
	}
	wait, _, cerr = settleValuesWith(t, "timeout", "0", "no-wait", "true")
	if cerr != nil || wait {
		t.Errorf("--no-wait = wait %v err %v, want false, nil (timeout is irrelevant without a wait)", wait, cerr)
	}
}

// settleValuesWith is settleOptions.values on a fresh command after setting
// the settle flags given as name, value pairs, in order.
func settleValuesWith(t *testing.T, flagValues ...string) (bool, time.Duration, *clierr.Error) {
	t.Helper()
	cmd := &cobra.Command{Use: "x"}
	var o settleOptions
	addSettleFlags(cmd, &o)
	for i := 0; i+1 < len(flagValues); i += 2 {
		if err := cmd.Flags().Set(flagValues[i], flagValues[i+1]); err != nil {
			t.Fatal(err)
		}
	}
	return o.values()
}

// TestWaitCreateStoppedFailsFast: a group created with autostart on can go
// from pending straight to stopped and never start. create --wait must fail
// on that poll, not after the whole --timeout.
func TestWaitCreateStoppedFailsFast(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	cerr, h := app.runWaitFrom(t, []waitStep{
		statusStep(shared.CONTAINER_GROUP_STATUS_STOPPED),
	}, true, 10, time.Hour)
	if cerr == nil {
		t.Fatal("waitForGroupStatus returned nil, want group_stopped")
	}
	if cerr.Kind != clierr.KindGroupStopped || cerr.Code != clierr.CodeGeneric {
		t.Errorf("error = kind %q code %d, want group_stopped / generic", cerr.Kind, cerr.Code)
	}
	if h.calls != 1 || len(h.sleeps) != 0 {
		t.Errorf("polled %d times and slept %d, want 1 and 0", h.calls, len(h.sleeps))
	}
	for _, want := range []string{"demo", "stopped", "run saladcloud container-groups start demo"} {
		if !strings.Contains(errText(cerr), want) {
			t.Errorf("message %q lacks %q", errText(cerr), want)
		}
	}
}

// TestWaitStartToleratesInitialStopped: right after start, the first polls
// can still read stopped because the API has not acted yet. That must not
// abort the wait.
func TestWaitStartToleratesInitialStopped(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	cerr, h := app.runWait(t, []waitStep{
		statusStep(shared.CONTAINER_GROUP_STATUS_STOPPED),
		statusStep(shared.CONTAINER_GROUP_STATUS_STOPPED),
		statusStep(shared.CONTAINER_GROUP_STATUS_PENDING),
		statusStep(shared.CONTAINER_GROUP_STATUS_DEPLOYING),
		statusStep(shared.CONTAINER_GROUP_STATUS_RUNNING),
	}, 10, time.Hour)
	if cerr != nil {
		t.Fatalf("waitForGroupStatus returned %v, want nil", cerr)
	}
	if h.calls != 5 {
		t.Errorf("get called %d times, want 5", h.calls)
	}
}

// TestWaitAbortsOnTerminalAfterActive: once the group has been seen active,
// stopped, failed and succeeded each end a wait for running at once.
func TestWaitAbortsOnTerminalAfterActive(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	for _, tc := range []struct {
		terminal shared.ContainerGroupStatus
		kind     clierr.Kind
	}{
		{shared.CONTAINER_GROUP_STATUS_STOPPED, "group_stopped"},
		{shared.CONTAINER_GROUP_STATUS_FAILED, "group_failed"},
		{shared.CONTAINER_GROUP_STATUS_SUCCEEDED, "group_succeeded"},
	} {
		t.Run(string(tc.terminal), func(t *testing.T) {
			t.Parallel()
			app := app.clone(t)
			cerr, h := app.runWait(t, []waitStep{
				statusStep(shared.CONTAINER_GROUP_STATUS_STOPPED), // start not yet acted on
				statusStep(shared.CONTAINER_GROUP_STATUS_DEPLOYING),
				statusStep(tc.terminal),
			}, 10, time.Hour)
			if cerr == nil || cerr.Kind != tc.kind || cerr.Code != clierr.CodeGeneric {
				t.Fatalf("error = %v, want kind %s / generic", cerr, tc.kind)
			}
			if h.calls != 3 {
				t.Errorf("get called %d times, want 3 (abort on the terminal poll)", h.calls)
			}
		})
	}
}

// TestAbortUnreachableOnlyForRunning: stop --wait targets stopped, so it
// gets no abort check at all.
func TestAbortUnreachableOnlyForRunning(t *testing.T) {
	t.Parallel()
	if abortUnreachable("demo", "stopped", true) != nil {
		t.Error("abortUnreachable for target stopped is non-nil, want nil")
	}
}
