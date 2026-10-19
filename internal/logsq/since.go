package logsq

import (
	"fmt"
	"regexp"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// dayPrefix matches a leading whole-day component ("3d", "1d12h") that Go's
// [time.ParseDuration] does not understand.
var dayPrefix = regexp.MustCompile(`^(\d+)d(.*)$`)

// ParseTimeRef turns a user-supplied time reference into an absolute UTC
// time. Accepted forms:
//   - Go durations, meaning now minus the duration: "15m", "2h", "90s"
//   - day-extended durations: "3d", "1d12h" (Nd is rewritten to (N*24)h)
//   - RFC3339 / RFC3339Nano timestamps: "2026-01-02T15:04:05Z"
//   - date-only "2006-01-02", meaning midnight UTC on that date
func ParseTimeRef(s string, now time.Time) (time.Time, *clierr.Error) {
	if s != "" {
		if t, ok := matchTimeRef(s, now); ok {
			return t, nil
		}
	}
	return time.Time{}, clierr.New(clierr.CodeUsage, clierr.KindUsage, `invalid time reference "%s"`, s).
		WithHint("use a duration like 15m, 2h or 3d, an RFC3339 timestamp like 2026-01-02T15:04:05Z, or a date like 2026-01-02")
}

// matchTimeRef is ParseTimeRef for a non-empty s, trying each accepted form in turn; ok is false when none fits.
func matchTimeRef(s string, now time.Time) (time.Time, bool) {
	if d, err := time.ParseDuration(expandDays(s)); err == nil {
		return now.Add(-d).UTC(), true
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

// expandDays rewrites a leading whole-day component of a duration as hours ("1d12h" is "24h12h"), leaving
// anything else as it is.
func expandDays(s string) string {
	m := dayPrefix.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	var days int
	if _, err := fmt.Sscanf(m[1], "%d", &days); err != nil {
		return s
	}
	return fmt.Sprintf("%dh%s", days*24, m[2])
}
