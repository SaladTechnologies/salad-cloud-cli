package logsq

import (
	"fmt"
	"regexp"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// dayPrefix matches a leading whole-day component ("3d", "1d12h") that Go's
// time.ParseDuration does not understand.
var dayPrefix = regexp.MustCompile(`^(\d+)d(.*)$`)

// ParseTimeRef turns a user-supplied time reference into an absolute UTC
// time. Accepted forms:
//   - Go durations, meaning now minus the duration: "15m", "2h", "90s"
//   - day-extended durations: "3d", "1d12h" (Nd is rewritten to (N*24)h)
//   - RFC3339 / RFC3339Nano timestamps: "2026-01-02T15:04:05Z"
//   - date-only "2006-01-02", meaning midnight UTC on that date
func ParseTimeRef(s string, now time.Time) (time.Time, *clierr.Error) {
	if s != "" {
		durStr := s
		if m := dayPrefix.FindStringSubmatch(s); m != nil {
			var days int
			if _, err := fmt.Sscanf(m[1], "%d", &days); err == nil {
				durStr = fmt.Sprintf("%dh%s", days*24, m[2])
			}
		}
		if d, err := time.ParseDuration(durStr); err == nil {
			return now.Add(-d).UTC(), nil
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UTC(), nil
		}
		if t, err := time.Parse(time.DateOnly, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, clierr.New(clierr.CodeUsage, clierr.KindUsage, `invalid time reference "%s"`, s).
		WithHint("use a duration like 15m, 2h or 3d, an RFC3339 timestamp like 2026-01-02T15:04:05Z, or a date like 2026-01-02")
}
