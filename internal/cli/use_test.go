package cli

import "testing"

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
			org, project, err := parseUseTarget(tt.arg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseUseTarget(%q) = (%q, %q, nil), want usage error", tt.arg, org, project)
				}
				if err.Code != 2 {
					t.Errorf("error code = %d, want 2 (usage)", err.Code)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseUseTarget(%q) error: %v", tt.arg, err)
			}
			if org != tt.org || project != tt.project {
				t.Errorf("parseUseTarget(%q) = (%q, %q), want (%q, %q)", tt.arg, org, project, tt.org, tt.project)
			}
		})
	}
}
