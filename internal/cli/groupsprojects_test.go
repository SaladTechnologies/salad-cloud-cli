package cli

import (
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// TestGroupsListProjects: cg list --projects skips a project the organization does not have, with a warning that
// names the organization, and fails as the organization when the key cannot see it, instead of reporting each
// project as not found.
func TestGroupsListProjects(t *testing.T) {
	t.Parallel()
	saved := &config.File{APIKey: testAPIKey, Organization: "my-org", Project: "default"}
	tests := []struct {
		name     string
		args     []string
		code     int
		want     string
		dontWant string
	}{
		{
			name:     "a missing project is skipped",
			args:     []string{"cg", "list", "--projects", "staging", "--projects", "nope"},
			code:     0,
			want:     `warning: project "nope" not found in organization my-org, skipped`,
			dontWant: "error:",
		},
		{
			name:     "an organization the key cannot see fails as the organization",
			args:     []string{"cg", "list", "--org", "gone-org", "--projects", "default", "--projects", "staging"},
			code:     4,
			want:     `organization "gone-org" (from --org) not found`,
			dontWant: "skipped",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := session{args: tc.args, saved: saved}.run(t)
			if o.code != tc.code {
				t.Fatalf("exit %d, want %d\nstderr:\n%s", o.code, tc.code, o.stderr)
			}
			if !strings.Contains(o.stderr, tc.want) {
				t.Errorf("stderr lacks %q:\n%s", tc.want, o.stderr)
			}
			if strings.Contains(o.stderr, tc.dontWant) {
				t.Errorf("stderr has %q:\n%s", tc.dontWant, o.stderr)
			}
		})
	}
}
