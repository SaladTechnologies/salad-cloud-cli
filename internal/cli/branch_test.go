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

func runBranches(t *testing.T, cases []branchCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			before := config.File{}
			if tc.s.saved != nil {
				before = *tc.s.saved
			}
			want := before
			if tc.after != nil {
				want = *tc.after
			}

			o := withMode(tc.s, "table").run(t)
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
			if tc.s.rawConfig == "" && o.config != want {
				t.Errorf("config file %+v, want %+v", o.config, want)
			}

			j := withMode(tc.s, "json").run(t)
			atJ := j.placeholders
			if j.code != tc.code || atJ(j.device) != tc.device {
				t.Errorf("json: exit %d, terminal %q; table mode had %d, %q", j.code, j.device, tc.code, tc.device)
			}
			checkMachineStderr(t, j)
			if tc.code != 0 {
				doc, err := j.errorDocument()
				if err != nil {
					t.Fatalf("json: %v\n%s", err, j.stderr)
				}
				msg, hint := tableError(at(o.stderr))
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
				return
			}
			doc := j.document(t)
			for k, v := range tc.doc {
				if got, ok := doc[k]; !ok || atJ(fmt.Sprint(got)) != fmt.Sprint(v) {
					t.Errorf("json %s = %v, want %v", k, got, v)
				}
			}
		})
	}
}
