package logsq

import (
	"testing"
	"time"
)

func TestFormatRequestTimeFloors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{"already ms", time.Date(2026, time.September, 1, 18, 13, 52, 0, time.UTC), "2026-09-01T18:13:52.000Z"},
		{
			"exact ms fraction",
			time.Date(2026, time.September, 1, 18, 13, 52, 123000000, time.UTC),
			"2026-09-01T18:13:52.123Z",
		},
		{
			"sub-ms floors",
			time.Date(2026, time.September, 1, 18, 13, 52, 555970600, time.UTC),
			"2026-09-01T18:13:52.555Z",
		},
		{
			"just under next ms floors",
			time.Date(2026, time.September, 1, 18, 13, 52, 999999999, time.UTC),
			"2026-09-01T18:13:52.999Z",
		},
		{
			"non-UTC converts",
			time.Date(2026, time.September, 1, 20, 13, 52, 1, time.FixedZone("CEST", 2*3600)),
			"2026-09-01T18:13:52.000Z",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := FormatRequestTime(tt.in); got != tt.want {
				t.Errorf("FormatRequestTime(%v) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestCeilRequestTime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{
			"already ms unchanged",
			time.Date(2026, time.September, 1, 18, 13, 52, 123000000, time.UTC),
			"2026-09-01T18:13:52.123Z",
		},
		{
			"whole second unchanged",
			time.Date(2026, time.September, 1, 18, 13, 52, 0, time.UTC),
			"2026-09-01T18:13:52.000Z",
		},
		{
			"sub-ms rounds up",
			time.Date(2026, time.September, 1, 18, 13, 52, 555970600, time.UTC),
			"2026-09-01T18:13:52.556Z",
		},
		{"1ns rounds up", time.Date(2026, time.September, 1, 18, 13, 52, 1, time.UTC), "2026-09-01T18:13:52.001Z"},
		{
			"carries into next second",
			time.Date(2026, time.September, 1, 18, 13, 52, 999999999, time.UTC),
			"2026-09-01T18:13:53.000Z",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CeilRequestTime(tt.in); got != tt.want {
				t.Errorf("CeilRequestTime(%v) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestRequestTimeFromAPIResponseTimestamps(t *testing.T) {
	t.Parallel()
	// Response timestamps carry 7 fractional digits and a +00:00 offset
	// (page_min_time/page_max_time included). Feeding one back raw would
	// 400 with invalid_time_range; the parse-then-FormatRequestTime round
	// trip must yield an accepted millisecond form. time.RFC3339Nano
	// handles both Z and numeric offsets at any fractional width.
	tests := []struct {
		in   string
		want string
	}{
		{"2026-09-01T18:13:47.5559706+00:00", "2026-09-01T18:13:47.555Z"},
		{"2026-09-01T18:13:47.5559706Z", "2026-09-01T18:13:47.555Z"},
		{"2026-09-01T18:13:47+00:00", "2026-09-01T18:13:47.000Z"},
		{"2026-09-01T20:13:47.5559706+02:00", "2026-09-01T18:13:47.555Z"},
		{"2026-09-01T18:13:47.5Z", "2026-09-01T18:13:47.500Z"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			parsed, err := time.Parse(time.RFC3339Nano, tt.in)
			if err != nil {
				t.Fatalf("parse(%q) error: %v", tt.in, err)
			}
			if got := FormatRequestTime(parsed); got != tt.want {
				t.Errorf("FormatRequestTime(parse(%q)) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}
