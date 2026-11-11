package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// readyStep is one scripted poll: the group's status and replicas, then its instances (unread when the poll ends
// before them), or an error for the group read.
type readyStep struct {
	status    shared.ContainerGroupStatus
	replicas  int64
	instances []containergroups.ContainerGroupInstance
	err       *clierr.Error
}

// inst is an instance on version in state, ready or not.
func inst(
	version int64,
	state containergroups.TheContainerGroupInstanceState,
	ready bool,
) containergroups.ContainerGroupInstance {
	return containergroups.ContainerGroupInstance{Version: &version, State: &state, Ready: &ready}
}

const (
	stRunning     = containergroups.THE_CONTAINER_GROUP_INSTANCE_STATE_RUNNING
	stDownloading = containergroups.THE_CONTAINER_GROUP_INSTANCE_STATE_DOWNLOADING
)

// readyHarness scripts the polls and drives a fake clock: each sleep advances now by the poll interval.
type readyHarness struct {
	t      *testing.T
	steps  []readyStep
	polls  int
	now    time.Time
	stderr strings.Builder
}

func (h *readyHarness) deps() readyDeps {
	return readyDeps{
		group: func(_ context.Context) (*shared.ContainerGroup, *clierr.Error) {
			if h.polls >= len(h.steps) {
				h.t.Fatalf("poll %d, only %d scripted", h.polls+1, len(h.steps))
			}
			s := h.steps[h.polls]
			h.polls++
			if s.err != nil {
				return nil, s.err
			}
			status := s.status
			return &shared.ContainerGroup{
				Name: new(
					"demo",
				),
				Replicas:     new(s.replicas),
				CurrentState: &shared.ContainerGroupState{Status: &status},
			}, nil
		},
		instances: func(_ context.Context) ([]containergroups.ContainerGroupInstance, *clierr.Error) {
			return h.steps[h.polls-1].instances, nil
		},
		now: func() time.Time { return h.now },
		sleep: func(_ context.Context, d time.Duration) error {
			h.now = h.now.Add(d)
			return nil
		},
		progress: &h.stderr,
	}
}

// runReady waits for version 2 on a scripted harness, polling every 10s until timeout.
func runReady(
	t *testing.T,
	app *testRun,
	steps []readyStep,
	readyFor, timeout time.Duration,
) (*shared.ContainerGroup, *clierr.Error, *readyHarness) {
	t.Helper()
	h := &readyHarness{t: t, steps: steps, now: time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC)}
	goal := readyGoal{group: "demo", version: 2, readyFor: readyFor, timeout: timeout}
	g, cerr := app.waitForReady(t.Context(), h.deps(), goal, h.now.Add(timeout), 10*time.Second)
	return g, cerr, h
}

func TestReadyWaitsForTheNewVersion(t *testing.T) {
	t.Parallel()
	running := shared.CONTAINER_GROUP_STATUS_RUNNING
	steps := []readyStep{
		// Right after the settle the old instance still runs and reads ready: not done.
		{status: running, replicas: 1, instances: []containergroups.ContainerGroupInstance{inst(1, stRunning, true)}},
		{
			status:    running,
			replicas:  1,
			instances: []containergroups.ContainerGroupInstance{inst(2, stDownloading, false)},
		},
		{status: running, replicas: 1, instances: []containergroups.ContainerGroupInstance{inst(2, stRunning, false)}},
		{status: running, replicas: 1, instances: []containergroups.ContainerGroupInstance{inst(2, stRunning, true)}},
	}
	g, cerr, h := runReady(t, newTestRun(t), steps, 0, time.Hour)
	if cerr != nil || g == nil {
		t.Fatalf("wait failed: %v", cerr)
	}
	if h.polls != 4 {
		t.Errorf("polls = %d, want 4", h.polls)
	}
	for _, want := range []string{
		"0 of 1 replicas ready on version 2 (instances: old version running 1)",
		"(instances: downloading 1)",
		"(instances: running, not ready 1)",
	} {
		if !strings.Contains(h.stderr.String(), want) {
			t.Errorf("progress lacks %q:\n%s", want, h.stderr.String())
		}
	}
}

func TestReadyCountsALaterVersion(t *testing.T) {
	t.Parallel()
	running := shared.CONTAINER_GROUP_STATUS_RUNNING
	steps := []readyStep{
		{status: running, replicas: 2, instances: []containergroups.ContainerGroupInstance{
			inst(3, stRunning, true), inst(2, stRunning, true),
		}},
	}
	if _, cerr, _ := runReady(t, newTestRun(t), steps, 0, time.Hour); cerr != nil {
		t.Fatalf("a later version should count: %v", cerr)
	}
}

func TestReadyForRestartsWhenReadinessDrops(t *testing.T) {
	t.Parallel()
	running := shared.CONTAINER_GROUP_STATUS_RUNNING
	ready := readyStep{
		status: running, replicas: 1,
		instances: []containergroups.ContainerGroupInstance{inst(2, stRunning, true)},
	}
	notReady := readyStep{
		status: running, replicas: 1,
		instances: []containergroups.ContainerGroupInstance{inst(2, stRunning, false)},
	}
	// Ready at 0s and 10s, not at 20s, then ready from 30s: 20s of ready from 30s ends the wait at 50s.
	steps := []readyStep{ready, ready, notReady, ready, ready, ready}
	_, cerr, h := runReady(t, newTestRun(t), steps, 20*time.Second, time.Hour)
	if cerr != nil {
		t.Fatalf("wait failed: %v", cerr)
	}
	if h.polls != 6 {
		t.Errorf("polls = %d, want 6 (the drop restarts the 20s)", h.polls)
	}
	if !strings.Contains(h.stderr.String(), "staying ready for 20s (10s so far)") {
		t.Errorf("progress lacks the hold:\n%s", h.stderr.String())
	}
}

func TestReadyTimeoutSaysTheUpdateWasApplied(t *testing.T) {
	t.Parallel()
	running := shared.CONTAINER_GROUP_STATUS_RUNNING
	pulling := readyStep{
		status: running, replicas: 1,
		instances: []containergroups.ContainerGroupInstance{inst(2, stDownloading, false)},
	}
	_, cerr, _ := runReady(t, newTestRun(t), []readyStep{pulling, pulling, pulling, pulling}, 0, 30*time.Second)
	if cerr == nil || cerr.Kind != clierr.KindWaitTimeout || cerr.Code != clierr.CodeGeneric {
		t.Fatalf("err = %v, want wait_timeout exit 1", cerr)
	}
	want := "update applied, but demo was not ready within --timeout 30s: 0 of 1 replicas ready on version 2 " +
		"(instances: downloading 1)"
	if cerr.Message != want {
		t.Errorf("message = %q\nwant      %q", cerr.Message, want)
	}
}

func TestReadyEndsOnAStoppedGroup(t *testing.T) {
	t.Parallel()
	_, cerr, h := runReady(t, newTestRun(t),
		[]readyStep{{status: shared.CONTAINER_GROUP_STATUS_STOPPED, replicas: 1}}, 0, time.Hour)
	if cerr == nil || cerr.Kind != clierr.KindGroupStopped {
		t.Fatalf("err = %v, want group_stopped", cerr)
	}
	if h.polls != 1 {
		t.Errorf("polls = %d, want 1", h.polls)
	}
}

func TestReadyWithNoReplicasIsDone(t *testing.T) {
	t.Parallel()
	g, cerr, _ := runReady(t, newTestRun(t),
		[]readyStep{{status: shared.CONTAINER_GROUP_STATUS_RUNNING, replicas: 0}}, 0, time.Hour)
	if cerr != nil || g == nil {
		t.Fatalf("err = %v, want done", cerr)
	}
}

func TestReadyToleratesATransientError(t *testing.T) {
	t.Parallel()
	running := shared.CONTAINER_GROUP_STATUS_RUNNING
	steps := []readyStep{
		{err: clierr.New(clierr.CodeNetwork, clierr.KindNetwork, "connection reset")},
		{status: running, replicas: 1, instances: []containergroups.ContainerGroupInstance{inst(2, stRunning, true)}},
	}
	_, cerr, h := runReady(t, newTestRun(t), steps, 0, time.Hour)
	if cerr != nil {
		t.Fatalf("wait failed: %v", cerr)
	}
	if !strings.Contains(h.stderr.String(), "warning: connection reset (still waiting)") {
		t.Errorf("no transient warning:\n%s", h.stderr.String())
	}
}

func TestReadyJSONModePrintsNoProgress(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	if cerr := app.printer.SetMode("json"); cerr != nil {
		t.Fatal(cerr)
	}
	running := shared.CONTAINER_GROUP_STATUS_RUNNING
	steps := []readyStep{
		{status: running, replicas: 1, instances: []containergroups.ContainerGroupInstance{inst(1, stRunning, true)}},
		{status: running, replicas: 1, instances: []containergroups.ContainerGroupInstance{inst(2, stRunning, true)}},
	}
	_, cerr, h := runReady(t, app, steps, 0, time.Hour)
	if cerr != nil {
		t.Fatalf("wait failed: %v", cerr)
	}
	if h.stderr.Len() != 0 {
		t.Errorf("json mode printed progress:\n%s", h.stderr.String())
	}
}

func TestReadyOptionsCheck(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		o      readyOptions
		noWait bool
		want   string // "" for no error
	}{
		{"off", readyOptions{wait: false, readyFor: 0}, false, ""},
		{"on", readyOptions{wait: true, readyFor: 5 * time.Minute}, false, ""},
		{
			"ready-for alone",
			readyOptions{wait: false, readyFor: time.Minute},
			false,
			"--ready-for requires --wait-ready",
		},
		{
			"negative ready-for",
			readyOptions{wait: true, readyFor: -time.Second},
			false,
			"--ready-for must be zero or more",
		},
		{
			"with --no-wait",
			readyOptions{wait: true, readyFor: 0},
			true,
			"--wait-ready and --no-wait cannot be combined",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cerr := tc.o.check(tc.noWait)
			switch {
			case tc.want == "" && cerr != nil:
				t.Errorf("unexpected error: %v", cerr)
			case tc.want != "" &&
				(cerr == nil || cerr.Code != clierr.CodeUsage || !strings.Contains(cerr.Message, tc.want)):
				t.Errorf("err = %v, want usage error containing %q", cerr, tc.want)
			}
		})
	}
}

func TestNewVersionNote(t *testing.T) {
	t.Parallel()
	group := func(version, replicas int64, status shared.ContainerGroupStatus) *shared.ContainerGroup {
		return &shared.ContainerGroup{
			Version: &version, Replicas: &replicas, CurrentState: &shared.ContainerGroupState{Status: &status},
		}
	}
	running, stopped := shared.CONTAINER_GROUP_STATUS_RUNNING, shared.CONTAINER_GROUP_STATUS_STOPPED
	tests := []struct {
		name          string
		before, after *shared.ContainerGroup
		waitReady     bool
		want          string
	}{
		{
			"new version", group(1, 1, running), group(2, 1, running), false,
			"note: demo is now version 2; its instances move onto it as their nodes check in, pulling the image " +
				"again; to wait until they are ready, pass --wait-ready",
		},
		{
			"new version, waiting", group(1, 1, running), group(2, 1, running), true,
			"note: demo is now version 2; its instances move onto it as their nodes check in, pulling the image again",
		},
		{"same version", group(2, 3, running), group(2, 4, running), false, ""},
		{"stopped", group(1, 1, stopped), group(2, 1, stopped), false, ""},
		{"no replicas", group(1, 0, running), group(2, 0, running), false, ""},
		{"no before", nil, group(2, 1, running), false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			settle := patchSettle{wait: true, timeout: time.Minute, doneMsg: "", waitReady: tc.waitReady}
			if got := settle.newVersionNote("demo", tc.before, tc.after); got != tc.want {
				t.Errorf("note = %q\nwant   %q", got, tc.want)
			}
		})
	}
}
