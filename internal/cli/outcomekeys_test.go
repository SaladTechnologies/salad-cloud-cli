package cli

import (
	"fmt"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// TestOutcomeFactsInJSON: facts login and doctor state on stderr in table
// mode are keys in their documents too.
func TestOutcomeFactsInJSON(t *testing.T) {
	K, O := testAPIKey, testOtherAPIKey
	login := func(saved *config.File, extra ...string) map[string]any {
		t.Helper()
		args := append([]string{"login", "--api-key", "-", "--org", "my-org", "-o", "json"}, extra...)
		o := session{args: args, stdin: K + "\n", saved: saved}.run(t)
		if o.code != 0 {
			t.Fatalf("%v: exit %d\n%s", args, o.code, o.stderr)
		}
		return o.document(t)
	}
	for _, tc := range []struct {
		name  string
		saved *config.File
		extra []string
		want  any
	}{
		{"the saved project is gone", &config.File{Organization: "my-org", Project: "gone"}, nil, false},
		{"the saved project is found", &config.File{Organization: "my-org", Project: "staging"}, nil, true},
		{"none saved", nil, nil, nil},
		{"--project names it", &config.File{Organization: "my-org", Project: "staging"}, []string{"--project", "default"}, nil},
	} {
		if got := login(tc.saved, tc.extra...)["saved_project_found"]; got != tc.want {
			t.Errorf("%s: saved_project_found = %v, want %v", tc.name, got, tc.want)
		}
	}

	// A login from SALAD_API_KEY names the saved key commands use where
	// the variable is not set.
	o := session{
		args: []string{
			"login",
			"-o",
			"json",
		},
		env:   map[string]string{"SALAD_API_KEY": K, "SALAD_ORGANIZATION": "my-org"},
		saved: &config.File{APIKey: O},
	}.run(t)
	if doc := o.document(t); doc["saved_api_key"] != "****...ABCD" || doc["api_key_saved"] != false {
		t.Errorf("saved_api_key = %v, api_key_saved = %v", doc["saved_api_key"], doc["api_key_saved"])
	}

	// doctor names the variables that hide a different saved value.
	d := session{
		args: []string{"doctor", "-o", "json"}, env: map[string]string{"SALAD_API_KEY": K},
		saved: &config.File{APIKey: O, Organization: "my-org", Project: "default"},
	}.run(t)
	if got := fmt.Sprint(d.document(t)["env_overrides"]); got != "[SALAD_API_KEY]" {
		t.Errorf("doctor env_overrides = %s, want [SALAD_API_KEY]", got)
	}
	d = session{
		args:  []string{"doctor", "-o", "json"},
		saved: &config.File{APIKey: K, Organization: "my-org", Project: "default"},
	}.run(
		t,
	)
	if got := fmt.Sprint(d.document(t)["env_overrides"]); got != "[]" {
		t.Errorf("doctor env_overrides with no variable = %s, want []", got)
	}
}
