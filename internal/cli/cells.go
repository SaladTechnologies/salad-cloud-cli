package cli

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// tableTime is how tables show a time: UTC to the second, one width for
// every row, instead of the API's variable-length fractions.
const tableTime = "2006-01-02 15:04:05Z"

// timeCell renders an API timestamp for a table: "-" when absent, the API's
// own text when it does not parse.
func timeCell(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return timeText(*s)
}

// timeText renders an RFC 3339 timestamp as tableTime, or returns it as is.
func timeText(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.UTC().Format(tableTime)
}

// jsonLogText renders a JSON log entry for a table line. An object with a
// "message" string (every system event, and many structured logs) reads as
// that message followed by its other non-empty fields as key=value; anything
// else is compact JSON. -o json keeps the entry whole.
func jsonLogText(v any) string {
	m, ok := v.(map[string]any)
	msg, isText := m["message"].(string)
	if !ok || !isText {
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
	keys := make([]string, 0, len(m))
	for k, val := range m {
		if k == "message" || val == nil || val == "" {
			continue
		}
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := []string{msg}
	for _, k := range keys {
		parts = append(parts, k+"="+jsonLogFieldText(m[k]))
	}
	return strings.Join(parts, " ")
}

// jsonLogFieldText renders one field value of a JSON log entry for
// jsonLogText: a string as is, anything else as compact JSON.
func jsonLogFieldText(val any) string {
	if s, isStr := val.(string); isStr {
		return s
	}
	b, err := json.Marshal(val)
	if err != nil {
		return fmt.Sprint(val)
	}
	return string(b)
}

// asType returns v as a T, or T's zero value when v holds something else (a
// field missing from a decoded document, or of another type).
func asType[T any](v any) T {
	if t, ok := v.(T); ok {
		return t
	}
	var zero T
	return zero
}
