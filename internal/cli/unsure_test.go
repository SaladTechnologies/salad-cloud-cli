package cli

import (
	"net/http"
	"strings"
	"testing"
)

// TestWriteThatMayHaveApplied: a write that reached the API and failed without saying whether it applied (a 504
// here) says so in its message, and its hint says how to check and whether sending it again is safe; a read that
// fails the same way says neither.
func TestWriteThatMayHaveApplied(t *testing.T) {
	t.Parallel()
	const group = "/containers/demo"
	for _, tc := range []unsureCase{
		{
			name: "update", args: []string{"cg", "update", "demo", "--image", "example/worker:new"},
			failOn: "PATCH " + group, applied: true,
			hintHas: []string{"saladcloud container-groups get demo", "new version and restarts the replicas"},
		},
		{
			name: "scale", args: []string{"cg", "scale", "demo", "--replicas", "3"},
			failOn: "PATCH " + group, applied: true,
			hintHas:  []string{"saladcloud container-groups get demo", "setting 3 replicas again is safe"},
			hintLack: []string{"scheduled scaling"},
		},
		{
			name: "scale --keep-schedule", args: []string{"cg", "scale", "demo", "--replicas", "3", "--keep-schedule"},
			failOn: "PATCH " + group, applied: true,
			hintHas: []string{"setting 3 replicas again is safe", "scheduled-scaling-enabled to true"},
		},
		{
			name: "delete", args: []string{"cg", "delete", "demo"},
			failOn: "DELETE " + group, applied: true,
			hintHas: []string{"check whether it took effect before you retry"},
		},
		{
			name: "get", args: []string{"cg", "get", "demo"},
			failOn: "GET " + group, applied: false,
		},
	} {
		for _, mode := range []string{"table", "json"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				t.Parallel()
				api := newFakeAPI()
				api.status = map[string]int{tc.failOn: http.StatusGatewayTimeout}
				o := withMode(session{args: tc.args, api: api, env: keyEnv()}, mode).run(t)
				tc.check(t, o, mode)
			})
		}
	}
}

// unsureCase is one command whose request at failOn fails with a 504: whether its message says the change may
// have been applied, and what its hint must and must not say.
type unsureCase struct {
	name     string
	args     []string
	failOn   string
	applied  bool
	hintHas  []string
	hintLack []string
}

// check checks tc's run o in mode.
func (tc *unsureCase) check(t *testing.T, o *outcome, mode string) {
	t.Helper()
	if o.code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", o.code, o.stderr)
	}
	got := runErrorText(t, o, mode)
	msg, hint := got.msg, got.hint
	if says := strings.Contains(msg, "may have been applied"); says != tc.applied {
		t.Errorf("message %q: says it may have been applied = %v, want %v", msg, says, tc.applied)
	}
	for _, want := range tc.hintHas {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q lacks %q", hint, want)
		}
	}
	for _, bad := range tc.hintLack {
		if strings.Contains(hint, bad) {
			t.Errorf("hint %q has %q", hint, bad)
		}
	}
	if !tc.applied && strings.Contains(hint, "retry") {
		t.Errorf("a failed read's hint talks about retrying a write: %q", hint)
	}
}

// TestCtrlCDuringAWrite: Ctrl-C while the API holds a write ends the command as canceled, says the change may have
// been applied, and keeps the command's hint, which Run would otherwise drop.
func TestCtrlCDuringAWrite(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"table", "json"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			api := newFakeAPI()
			api.cancelOn = "PATCH /containers/demo"
			o := withMode(session{
				args: []string{"cg", "update", "demo", "--image", "example/worker:new"}, api: api, env: keyEnv(),
			}, mode).run(t)
			if o.code != 1 {
				t.Fatalf("exit %d, want 1:\n%s", o.code, o.stderr)
			}
			got := runErrorText(t, o, mode)
			msg, hint := got.msg, got.hint
			if !strings.HasPrefix(msg, "container-groups update canceled; ") ||
				!strings.Contains(msg, "may have been applied") {
				t.Errorf("message = %q, want the cancel and that it may have been applied", msg)
			}
			if !strings.Contains(hint, "saladcloud container-groups get demo") {
				t.Errorf("hint = %q, want update's", hint)
			}
		})
	}
}

// runErrorText is the message and hint of a failed run, from the error document in json mode and from the
// "saladcloud: error:" and "hint:" lines in table mode.
func runErrorText(t *testing.T, o *outcome, mode string) runError {
	t.Helper()
	if mode == "json" {
		doc, err := o.errorDocument()
		if err != nil {
			t.Fatalf("%v:\n%s", err, o.stderr)
		}
		if doc["kind"] == nil {
			t.Errorf("error document has no kind: %v", doc)
		}
		return runError{msg: docString(doc, "message"), hint: docString(doc, "hint")}
	}
	var got runError
	for line := range strings.SplitSeq(o.stderr, "\n") {
		if m, ok := strings.CutPrefix(line, "saladcloud: error: "); ok {
			got.msg = m
		}
		if h, ok := strings.CutPrefix(line, "hint: "); ok {
			got.hint = h
		}
	}
	if got.msg == "" {
		t.Fatalf("no error line in stderr:\n%s", o.stderr)
	}
	return got
}

// runError is a failed run's error message and its hint ("" when there is none).
type runError struct {
	msg, hint string
}

// docString is the string at key in an error document, "" when it is null or not a string.
func docString(doc map[string]any, key string) string {
	if v, ok := doc[key].(string); ok {
		return v
	}
	return ""
}
