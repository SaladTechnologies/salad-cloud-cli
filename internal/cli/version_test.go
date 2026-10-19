package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/version"
)

// TestVersionJSONShape pins version's JSON document: every key present,
// including the beta stage and the issues URL bug reports should go to.
func TestVersionJSONShape(t *testing.T) {
	app := newTestRun(t)
	out, err := app.runRoot(t, "version", "-o", "json")
	if err != nil {
		t.Fatalf("version -o json: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output is not one JSON document: %v\n%s", err, out)
	}
	for _, k := range []string{"version", "commit", "os", "arch", "stage", "issues_url"} {
		if _, ok := m[k]; !ok {
			t.Errorf("key %q missing from %s", k, out)
		}
	}
	if len(m) != 6 {
		t.Errorf("got %d keys, want 6", len(m))
	}
	if got := string(m["stage"]); got != `"`+version.Stage+`"` {
		t.Errorf("stage = %s, want %q", got, version.Stage)
	}
	if got := string(m["issues_url"]); !strings.Contains(got, "github.com/SaladTechnologies/salad-cloud-cli/issues") {
		t.Errorf("issues_url = %s", got)
	}
}

// TestRootHelpNamesBetaAndIssues: --help is where a new user looks first.
func TestRootHelpNamesBetaAndIssues(t *testing.T) {
	app := newTestRun(t)
	for _, want := range []string{version.Stage + " release", version.IssuesURL, "saladcloud version"} {
		if !strings.Contains(app.root.Long, want) {
			t.Errorf("root --help lacks %q", want)
		}
	}
}
