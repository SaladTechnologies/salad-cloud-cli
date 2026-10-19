package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/inferenceendpoints"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// jobWithStatus builds a minimal job in the given status.
func jobWithStatus(id string, status inferenceendpoints.Status) *inferenceendpoints.InferenceEndpointJob {
	j := &inferenceendpoints.InferenceEndpointJob{Id: ptr.To(id)}
	j.SetStatus(status)
	return j
}

// scriptedJobWaitDeps returns deps whose get walks the given status sequence
// (sticking on the last entry) against a fake clock that sleep advances.
func scriptedJobWaitDeps(
	statuses []inferenceendpoints.Status,
	progress *bytes.Buffer,
) jobWaitDeps[inferenceendpoints.InferenceEndpointJob] {
	clock := time.Date(2026, time.September, 2, 0, 0, 0, 0, time.UTC)
	i := 0
	return jobWaitDeps[inferenceendpoints.InferenceEndpointJob]{
		view:  inferenceJobView,
		where: `on endpoint "transcribe"`,
		get: func(_ context.Context) (*inferenceendpoints.InferenceEndpointJob, *clierr.Error) {
			s := statuses[i]
			if i < len(statuses)-1 {
				i++
			}
			return jobWithStatus("job-1", s), nil
		},
		now: func() time.Time { return clock },
		sleep: func(_ context.Context, d time.Duration) error {
			clock = clock.Add(d)
			return nil
		},
		progress: progress,
		label:    "waiting for job job-1",
	}
}

func TestWaitForJobTerminalSucceeded(t *testing.T) {
	var progress bytes.Buffer
	d := scriptedJobWaitDeps([]inferenceendpoints.Status{
		inferenceendpoints.STATUS_PENDING,
		inferenceendpoints.STATUS_RUNNING,
		inferenceendpoints.STATUS_SUCCEEDED,
	}, &progress)
	job, cerr := waitForJobTerminal(t.Context(), d, "job-1", time.Minute, time.Second)
	if cerr != nil {
		t.Fatalf("waitForJobTerminal: %v", cerr)
	}
	if job == nil || jobStatusString(job) != "succeeded" {
		t.Errorf("job status = %s, want succeeded", jobStatusString(job))
	}
	// One progress line per status CHANGE, none for the terminal status.
	out := progress.String()
	if got := strings.Count(out, "pending"); got != 1 {
		t.Errorf("progress mentions pending %d times, want 1:\n%s", got, out)
	}
	if got := strings.Count(out, "running"); got != 1 {
		t.Errorf("progress mentions running %d times, want 1:\n%s", got, out)
	}
	if strings.Contains(out, "succeeded") {
		t.Errorf("progress mentions the terminal status, want none:\n%s", out)
	}
}

func TestWaitForJobTerminalFailed(t *testing.T) {
	var progress bytes.Buffer
	d := scriptedJobWaitDeps([]inferenceendpoints.Status{
		inferenceendpoints.STATUS_PENDING,
		inferenceendpoints.STATUS_FAILED,
	}, &progress)
	job, cerr := waitForJobTerminal(t.Context(), d, "job-1", time.Minute, time.Second)
	if cerr == nil {
		t.Fatal("waitForJobTerminal = nil error, want a failure naming the status")
	}
	if cerr.Code != clierr.CodeGeneric {
		t.Errorf("Code = %d, want %d", cerr.Code, clierr.CodeGeneric)
	}
	if !strings.Contains(cerr.Message, "failed") || !strings.Contains(cerr.Message, "job-1") {
		t.Errorf("Message = %q, want it to name the job and the failed status", cerr.Message)
	}
	if job == nil {
		t.Error("job = nil, want the final job returned alongside the error")
	}
}

func TestWaitForJobTerminalTimeout(t *testing.T) {
	var progress bytes.Buffer
	d := scriptedJobWaitDeps([]inferenceendpoints.Status{
		inferenceendpoints.STATUS_RUNNING,
	}, &progress)
	_, cerr := waitForJobTerminal(t.Context(), d, "job-1", 10*time.Second, 5*time.Second)
	if cerr == nil {
		t.Fatal("waitForJobTerminal = nil error, want wait_timeout")
	}
	if cerr.Kind != clierr.KindWaitTimeout {
		t.Errorf("Kind = %q, want wait_timeout", cerr.Kind)
	}
	if !strings.Contains(cerr.Message, "running") {
		t.Errorf("Message = %q, want it to report the last status", cerr.Message)
	}
}

// TestWaitForJobTransientErrorsKeepPolling: network/5xx errors must warn and
// continue; the wait still succeeds once the job does.
func TestWaitForJobTransientErrorsKeepPolling(t *testing.T) {
	var progress bytes.Buffer
	clock := time.Date(2026, time.September, 2, 0, 0, 0, 0, time.UTC)
	calls := 0
	d := jobWaitDeps[inferenceendpoints.InferenceEndpointJob]{
		view: inferenceJobView,
		get: func(_ context.Context) (*inferenceendpoints.InferenceEndpointJob, *clierr.Error) {
			calls++
			if calls == 1 {
				return nil, clierr.New(clierr.CodeNetwork, clierr.KindNetwork, "connection reset")
			}
			return jobWithStatus("job-1", inferenceendpoints.STATUS_SUCCEEDED), nil
		},
		now: func() time.Time { return clock },
		sleep: func(_ context.Context, dur time.Duration) error {
			clock = clock.Add(dur)
			return nil
		},
		progress: &progress,
		label:    "waiting",
	}
	job, cerr := waitForJobTerminal(t.Context(), d, "job-1", time.Minute, time.Second)
	if cerr != nil {
		t.Fatalf("waitForJobTerminal: %v", cerr)
	}
	if jobStatusString(job) != "succeeded" {
		t.Errorf("status = %s, want succeeded", jobStatusString(job))
	}
	if !strings.Contains(progress.String(), "still waiting") {
		t.Errorf("progress = %q, want a transient-error warning", progress.String())
	}
}

// TestWaitForJobFatalErrorAborts: a 404 must abort immediately — waiting
// longer cannot fix it.
func TestWaitForJobFatalErrorAborts(t *testing.T) {
	d := jobWaitDeps[inferenceendpoints.InferenceEndpointJob]{
		view: inferenceJobView,
		get: func(_ context.Context) (*inferenceendpoints.InferenceEndpointJob, *clierr.Error) {
			return nil, clierr.New(clierr.CodeNotFound, clierr.KindNotFound, "job not found")
		},
		now:   time.Now,
		sleep: func(_ context.Context, _ time.Duration) error { return nil },
		label: "waiting",
	}
	_, cerr := waitForJobTerminal(t.Context(), d, "job-1", time.Minute, time.Second)
	if cerr == nil || cerr.Code != clierr.CodeNotFound {
		t.Fatalf("error = %v, want the not_found error passed through", cerr)
	}
}

func TestCompactJSON(t *testing.T) {
	if got := compactJSON(nil, 200); got != "-" {
		t.Errorf("compactJSON(nil) = %q, want -", got)
	}
	v := map[string]any{"text": "hello"}
	if got := compactJSON(v, 200); got != `{"text":"hello"}` {
		t.Errorf("compactJSON = %q, want compact object", got)
	}
	long := map[string]any{"text": strings.Repeat("a", 500)}
	got := compactJSON(long, 200)
	if len(got) > 200+len("...") || !strings.HasSuffix(got, "...") {
		t.Errorf("compactJSON long = %d bytes (%q...), want truncated to 200 with ellipsis", len(got), got[:20])
	}
}

// The limit note says when older jobs were left out: at the cap in every
// mode (the caller asked for more than one listing returns), below it in
// table mode when --limit kept the newest.
func TestJobsLimitNote(t *testing.T) {
	for _, tc := range []struct {
		mode         string
		shown, limit int
		more         bool
		want         string
	}{
		{"json", 10000, 0, true, "note: showing the newest 10000 jobs, the most one listing returns; older jobs are " +
			"not shown\n"},
		{"json", 10000, 20000, true, "note: showing the newest 10000 jobs, the most one listing returns; older jobs " +
			"are not shown\n"},
		{"table", 3, 3, true, "note: showing the newest 3 jobs; older ones exist\nhint: for more, raise --limit; 0 " +
			"means all, up to 10000\n"},
		{"json", 3, 3, true, ""},
		{"table", 39, 100, false, ""},
		{"table", 10000, 10000, false, ""},
	} {
		if err := printer.SetMode(tc.mode); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		jobsLimitNote(&buf, tc.shown, tc.limit, 10000, tc.more)
		if buf.String() != tc.want {
			t.Errorf(
				"%s, shown %d, limit %d, more %v: note %q, want %q",
				tc.mode,
				tc.shown,
				tc.limit,
				tc.more,
				buf.String(),
				tc.want,
			)
		}
	}
	setOutputMode(t, "table")
}

// TestJobTablesShareTheirWording: both job tables word the events row the
// same way and say input or output was cut only when it was.
func TestJobTablesShareTheirWording(t *testing.T) {
	var rows []string
	row := func(k, v string) { rows = append(rows, k+"="+v) }
	writeJobBody(row, map[string]any{"x": 1}, nil, 1, " (last event: created @ t)")
	if got := strings.Join(rows, "; "); got != `Input={"x":1}; Output=-; Events=1 (last event: created @ t)` {
		t.Errorf("short job rows = %s", got)
	}
	rows = nil
	writeJobBody(row, map[string]any{"x": strings.Repeat("a", 300)}, nil, 0, "")
	if last := rows[len(rows)-1]; last != "Note=Input and output are cut short here; -o json shows the full document" {
		t.Errorf("a cut input has no note: %v", rows)
	}
}
