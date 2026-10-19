package version

import "testing"

func TestModuleVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		main, want string
	}{
		{"", "dev"},
		{"(devel)", "dev"},
		{"v0.1.0-beta.1", "v0.1.0-beta.1"},
	}
	for _, tt := range tests {
		if got := moduleVersion(tt.main, "dev"); got != tt.want {
			t.Errorf("moduleVersion(%q) = %q, want %q", tt.main, got, tt.want)
		}
	}
}
