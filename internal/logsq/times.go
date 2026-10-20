package logsq

import "time"

// requestTimeLayout is the only timestamp form the log-entries endpoint
// accepts for start_time/end_time: exactly millisecond precision with a Z
// offset. Anything finer is rejected with invalid_time_range ("Log queries
// only support millisecond precision"), so response timestamps — which come
// back with 7 fractional digits and a +00:00 offset — must never be echoed
// back raw; parse them (time.RFC3339Nano handles both offset forms and any
// fractional width) and re-render through the helpers below.
const requestTimeLayout = "2006-01-02T15:04:05.000Z"

// FormatRequestTime floors t to millisecond precision and renders the only
// timestamp form the log-entries endpoint accepts. Use it for start times:
// flooring an inclusive start can only widen the window backward, never
// exclude an entry.
func FormatRequestTime(t time.Time) string {
	return t.UTC().Truncate(time.Millisecond).Format(requestTimeLayout)
}

// CeilRequestTime is FormatRequestTime with t rounded UP to the next
// millisecond when it carries sub-millisecond precision. Use it for end
// times so a caller-supplied "until" never excludes an entry sitting inside
// the final, partially-specified millisecond.
func CeilRequestTime(t time.Time) string {
	return ceilMillisecond(t).Format(requestTimeLayout)
}

// ceilMillisecond is t in UTC, rounded up to the next millisecond when it carries sub-millisecond precision.
func ceilMillisecond(t time.Time) time.Time {
	u := t.UTC()
	if f := u.Truncate(time.Millisecond); !f.Equal(u) {
		u = f.Add(time.Millisecond)
	}
	return u
}
