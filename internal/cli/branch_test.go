package cli

import (
	"fmt"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// branchCase is one command line and what it must print, run in table mode
// (exact stdout, stderr and terminal; the config file afterward) and in json
// mode (the error document, or the fields doc names; nothing on stderr but
// warnings and notes).
type branchCase struct {
	id     string
	s      session
	code   int
	kind   string // the error document's kind; "" on success
	stdout string // table mode; <path> stands for the config file
	stderr string
	device string
	opened int
	after  *config.File   // the config file afterward; nil: unchanged
	doc    map[string]any // json mode: fields of the document on success
	// jsonHint is the error document's hint when it differs from the
	// table's hint: line.
	jsonHint string
	// status is the error document's http_status, the status of the
	// response that caused the error; 0 means null (no response did).
	status int
}

// withDeps gives every case's session the seams set sets.
func withDeps(set func(d *Deps), cases []branchCase) []branchCase {
	for i := range cases {
		cases[i].s.deps = set
	}
	return cases
}

// runBranches runs each case as a parallel subtest, once in table mode and once in json mode, and checks both runs
// against what the case expects.
func runBranches(t *testing.T, cases []branchCase) {
	t.Helper()
	for i := range cases {
		tc := &cases[i]
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			o := withMode(tc.s, "table").run(t)
			checkBranchTable(t, tc, &o)

			j := withMode(tc.s, "json").run(t)
			if j.code != tc.code || j.placeholders(j.device) != tc.device {
				t.Errorf("json: exit %d, terminal %q; table mode had %d, %q", j.code, j.device, tc.code, tc.device)
			}
			checkMachineStderr(t, j)
			if tc.code != 0 {
				checkBranchErrorDocument(t, tc, &o, &j)
				return
			}
			checkBranchDocument(t, tc, &j)
		})
	}
}

// branchConfigAfter is the config file tc expects after its run: the saved file, or tc.after when the run changes
// it.
func branchConfigAfter(tc *branchCase) config.File {
	want := config.File{}
	if tc.s.saved != nil {
		want = *tc.s.saved
	}
	if tc.after != nil {
		want = *tc.after
	}
	return want
}

// checkBranchTable checks a case's table-mode run: exit code, exact stdout, stderr and terminal, the pages opened,
// and the config file afterward.
func checkBranchTable(t *testing.T, tc *branchCase, o *outcome) {
	t.Helper()
	at := o.placeholders
	if o.code != tc.code {
		t.Errorf("table: exit %d, want %d\nstderr:\n%s", o.code, tc.code, o.stderr)
	}
	if got := at(o.stdout); got != tc.stdout {
		t.Errorf("table stdout:\n%s\nwant:\n%s", got, tc.stdout)
	}
	if got := at(o.stderr); got != tc.stderr {
		t.Errorf("table stderr:\n%s\nwant:\n%s", got, tc.stderr)
	}
	if at(o.device) != tc.device {
		t.Errorf("terminal:\n%s\nwant:\n%s", o.device, tc.device)
	}
	if len(o.opened) != tc.opened {
		t.Errorf("opened %v, want %d pages", o.opened, tc.opened)
	}
	if want := branchConfigAfter(tc); tc.s.rawConfig == "" && o.config != want {
		t.Errorf("config file %+v, want %+v", o.config, want)
	}
}

// checkBranchErrorDocument checks a failing case's json-mode error document against its table-mode run o: the same
// message and hint (or tc.jsonHint), the case's kind, and its http_status.
func checkBranchErrorDocument(t *testing.T, tc *branchCase, o, j *outcome) {
	t.Helper()
	atJ := j.placeholders
	doc, err := j.errorDocument()
	if err != nil {
		t.Fatalf("json: %v\n%s", err, j.stderr)
	}
	msg, hint := tableError(o.placeholders(o.stderr))
	if tc.jsonHint != "" {
		hint = tc.jsonHint
	}
	if doc["kind"] != tc.kind || atJ(fmt.Sprint(doc["message"])) != msg ||
		atJ(fmt.Sprint(doc["hint"])) != hint {
		t.Errorf("json error %v; want kind %s, message %q, hint %q", doc, tc.kind, msg, hint)
	}
	var status any
	if tc.status != 0 {
		status = float64(tc.status)
	}
	if doc["http_status"] != status {
		t.Errorf("json http_status = %v, want %v", doc["http_status"], status)
	}
}

// checkBranchDocument checks that a succeeding case's json-mode document has the fields tc.doc names, with their
// values.
func checkBranchDocument(t *testing.T, tc *branchCase, j *outcome) {
	t.Helper()
	doc := j.document(t)
	for k, v := range tc.doc {
		if got, ok := doc[k]; !ok || j.placeholders(fmt.Sprint(got)) != fmt.Sprint(v) {
			t.Errorf("json %s = %v, want %v", k, got, v)
		}
	}
}
