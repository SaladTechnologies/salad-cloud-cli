// Package changelog parses the repository's Keep-a-Changelog formatted
// CHANGELOG.md into releases so `saladcloud changelog --since` can return
// only what a caller has not seen.
package changelog

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Release is one "## [version] - date" section of the changelog.
type Release struct {
	Version string  `json:"version"` // "Unreleased" for the unreleased section
	Date    *string `json:"date"`    // YYYY-MM-DD, or nil for Unreleased
	Body    string  `json:"body"`    // markdown between this heading and the next release heading, trimmed
	Title   *string `json:"title"`   // free text after the date on the heading line, or nil
}

// Unreleased reports whether the release is the unreleased section.
func (r Release) Unreleased() bool { return r.Version == "Unreleased" }

// headingRE matches "## [1.2.3] - 2026-09-15", "## [1.2.3] — 2026-09-15 title",
// and "## [Unreleased] — first release". The date is optional.
var headingRE = regexp.MustCompile(`^## \[([^\]]+)\](?:\s*[-—–]\s*(\d{4}-\d{2}-\d{2})?\s*(.*))?\s*$`)

// Parse splits markdown into releases in file order (newest first, as Keep a
// Changelog prescribes). Text before the first release heading is dropped.
func Parse(markdown string) []Release {
	var releases []Release
	var cur *Release
	var body []string
	flush := func() {
		if cur != nil {
			cur.Body = strings.TrimSpace(strings.Join(body, "\n"))
			releases = append(releases, *cur)
		}
		body = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		if m := headingRE.FindStringSubmatch(line); m != nil {
			flush()
			r := Release{Version: m[1]}
			if t := strings.TrimSpace(m[3]); t != "" {
				r.Title = &t
			}
			if m[2] != "" {
				d := m[2]
				r.Date = &d
			}
			cur = &r
			continue
		}
		if cur != nil {
			body = append(body, line)
		}
	}
	flush()
	return releases
}

// ErrBadSince is returned when --since is neither a date nor a version.
var ErrBadSince = errors.New("--since must be a date (YYYY-MM-DD) or a version (X.Y.Z)")

// Since filters releases to those newer than the cutoff. The cutoff is either
// a date (releases dated strictly after it) or a version (releases with a
// strictly greater semver). The Unreleased section is always included, since
// by definition the caller has not seen it. An empty since returns everything.
func Since(releases []Release, since string) ([]Release, error) {
	since = strings.TrimSpace(strings.TrimPrefix(since, "v"))
	if since == "" {
		return releases, nil
	}
	if d, err := time.Parse("2006-01-02", since); err == nil {
		var out []Release
		for _, r := range releases {
			if r.Unreleased() {
				out = append(out, r)
				continue
			}
			if r.Date == nil {
				continue
			}
			rd, err := time.Parse("2006-01-02", *r.Date)
			if err == nil && rd.After(d) {
				out = append(out, r)
			}
		}
		return out, nil
	}
	want, ok := parseSemver(since)
	if !ok {
		return nil, ErrBadSince
	}
	var out []Release
	for _, r := range releases {
		if r.Unreleased() {
			out = append(out, r)
			continue
		}
		v, ok := parseSemver(r.Version)
		if ok && compare(v, want) > 0 {
			out = append(out, r)
		}
	}
	return out, nil
}

// Render prints releases back as markdown, for table (human) mode.
func Render(releases []Release) string {
	var b strings.Builder
	for i, r := range releases {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "## [%s]", r.Version)
		if r.Date != nil {
			fmt.Fprintf(&b, " - %s", *r.Date)
		}
		if r.Title != nil {
			fmt.Fprintf(&b, " - %s", *r.Title)
		}
		b.WriteString("\n\n")
		b.WriteString(r.Body)
		b.WriteString("\n")
	}
	return b.String()
}

func parseSemver(s string) ([3]int, bool) {
	var v [3]int
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 { // drop pre-release / build metadata
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 1 || len(parts) > 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func compare(a, b [3]int) int {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			if a[i] > b[i] {
				return 1
			}
			return -1
		}
	}
	return 0
}
