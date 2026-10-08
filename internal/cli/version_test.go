package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/version"
)

// TestVersionJSONShape pins version's JSON document: every key present,
// including the beta stage and the issues URL bug reports should go to.
func TestVersionJSONShape(t *testing.T) {
	var buf bytes.Buffer
	saved := output.Stdout
	output.Stdout = &buf
	// -o json sets the package-wide output mode; restore table mode so later
	// tests that expect table-mode progress lines are unaffected.
	defer func() { output.Stdout = saved; _ = output.SetMode("table") }()

	rootCmd.SetArgs([]string{"version", "-o", "json"})
	defer rootCmd.SetArgs(nil)
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("version -o json: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("output is not one JSON document: %v\n%s", err, buf.String())
	}
	for _, k := range []string{"version", "commit", "os", "arch", "stage", "issues_url"} {
		if _, ok := m[k]; !ok {
			t.Errorf("key %q missing from %s", k, buf.String())
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
	for _, want := range []string{version.Stage + " release", version.IssuesURL, "saladcloud version"} {
		if !strings.Contains(rootCmd.Long, want) {
			t.Errorf("root --help lacks %q", want)
		}
	}
}
