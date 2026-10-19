package cli

import (
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

func TestParseUseTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		arg     string
		org     string
		project string
		wantErr bool
	}{
		{name: "org only", arg: "my-org", org: "my-org"},
		{name: "org and project", arg: "my-org/my-project", org: "my-org", project: "my-project"},
		{name: "trailing slash", arg: "my-org/", wantErr: true},
		{name: "empty", arg: "", wantErr: true},
		{name: "leading slash", arg: "/my-project", wantErr: true},
		{name: "multiple slashes", arg: "a/b/c", wantErr: true},
		{name: "bare slash", arg: "/", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			target, err := parseUseTarget(tt.arg)
			org, project := target.org, target.project
			code, wantCode := 0, 0 // an error is a usage error (2), and returns no names
			if err != nil {
				code = err.Code
			}
			if tt.wantErr {
				wantCode = clierr.CodeUsage
			}
			if code != wantCode || org != tt.org || project != tt.project {
				t.Errorf("parseUseTarget(%q) = (%q, %q, exit %d), want (%q, %q, exit %d)",
					tt.arg, org, project, code, tt.org, tt.project, wantCode)
			}
		})
	}
}
