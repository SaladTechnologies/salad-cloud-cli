package logsq

import (
	"strings"
	"testing"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

func TestParseTimeRef(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in   string
		want time.Time
	}{
		{"15m", now.Add(-15 * time.Minute)},
		{"2h", now.Add(-2 * time.Hour)},
		{"90s", now.Add(-90 * time.Second)},
		{"1h30m", now.Add(-90 * time.Minute)},
		{"3d", now.Add(-72 * time.Hour)},
		{"1d12h", now.Add(-36 * time.Hour)},
		{"2026-01-02T03:04:05Z", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		{"2026-01-02T03:04:05.123456789Z", time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)},
		{"2026-01-02T03:04:05+02:00", time.Date(2026, 1, 2, 1, 4, 5, 0, time.UTC)},
		{"2026-01-02", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseTimeRef(tt.in, now)
			if err != nil {
				t.Fatalf("ParseTimeRef(%q) error: %v", tt.in, err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("ParseTimeRef(%q) = %v, want %v", tt.in, got, tt.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("ParseTimeRef(%q) not in UTC: %v", tt.in, got.Location())
			}
		})
	}
}

func TestParseTimeRefGarbage(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	for _, in := range []string{"", "yesterday", "12x", "d", "2026-13-45", "15", "one hour"} {
		t.Run("garbage_"+in, func(t *testing.T) {
			_, err := ParseTimeRef(in, now)
			if err == nil {
				t.Fatalf("ParseTimeRef(%q) should fail", in)
			}
			if err.Code != clierr.CodeUsage {
				t.Errorf("code = %d, want %d", err.Code, clierr.CodeUsage)
			}
			// The message should teach the accepted forms.
			for _, hint := range []string{"15m", "RFC3339"} {
				if !strings.Contains(err.Message+" "+err.HintText(), hint) {
					t.Errorf("error message should mention %q: %s", hint, err.Message)
				}
			}
		})
	}
}
