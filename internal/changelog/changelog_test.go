package changelog

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	saladcloudcli "github.com/saladtechnologies/salad-cloud-cli"
)

const sample = `# Changelog

Preamble that is not part of any release.

## [Unreleased] — first release

### Added
- unreleased thing

## [1.2.0] - 2026-10-01

- one two

## [1.1.0] — 2026-09-15 the middle one

- one point one

## [1.0.0] - 2026-09-01

- one oh
`

func TestParse(t *testing.T) {
	t.Parallel()
	rs := Parse(sample)
	want := []string{
		`version Unreleased, date <nil>, title first release, body "### Added\n- unreleased thing"`,
		`version 1.2.0, date 2026-10-01, title <nil>, body "- one two"`,
		`version 1.1.0, date 2026-09-15, title the middle one, body "- one point one"`,
		`version 1.0.0, date 2026-09-01, title <nil>, body "- one oh"`,
	}
	if len(rs) != len(want) {
		t.Fatalf("got %d releases, want %d: %+v", len(rs), len(want), rs)
	}
	for i, r := range rs {
		if got := describe(r); got != want[i] {
			t.Errorf("release %d = %s, want %s", i, got, want[i])
		}
	}
}

// describe renders a release's fields on one line, an unset one as <nil>.
func describe(r Release) string {
	opt := func(s *string) string {
		if s == nil {
			return "<nil>"
		}
		return *s
	}
	return fmt.Sprintf("version %s, date %s, title %s, body %q", r.Version, opt(r.Date), opt(r.Title), r.Body)
}

func TestParseCRLF(t *testing.T) {
	t.Parallel()
	rs := Parse(strings.ReplaceAll(sample, "\n", "\r\n"))
	if len(rs) != 4 || strings.Contains(rs[3].Body, "\r") {
		t.Errorf("CRLF parse: %+v", rs)
	}
}

func versions(rs []Release) string {
	v := make([]string, 0, len(rs))
	for _, r := range rs {
		v = append(v, r.Version)
	}
	return strings.Join(v, ",")
}

func TestSince(t *testing.T) {
	t.Parallel()
	rs := Parse(sample)
	cases := []struct{ since, want string }{
		{"", "Unreleased,1.2.0,1.1.0,1.0.0"},
		{"2026-09-01", "Unreleased,1.2.0,1.1.0"}, // strictly after the date
		{"2026-09-15", "Unreleased,1.2.0"},
		{"2026-12-31", "Unreleased"},
		{"1.0.0", "Unreleased,1.2.0,1.1.0"}, // strictly greater version
		{"v1.1.0", "Unreleased,1.2.0"},
		{"1.2.0", "Unreleased"},
		{"0.9", "Unreleased,1.2.0,1.1.0,1.0.0"},
	}
	for _, c := range cases {
		got, err := Since(rs, c.since)
		if err != nil {
			t.Errorf("since %q: %v", c.since, err)
			continue
		}
		if versions(got) != c.want {
			t.Errorf("since %q = %s, want %s", c.since, versions(got), c.want)
		}
	}
	if _, err := Since(rs, "yesterday"); !errors.Is(err, ErrBadSince) {
		t.Errorf("bad since: err = %v, want ErrBadSince", err)
	}
}

// TestSincePreReleases: pre-release versions order as SemVer does (0.2.0-rc.1 after 0.1.0 and before 0.2.0, beta.11
// after beta.2), so --since a pre-release keeps the releases after it.
func TestSincePreReleases(t *testing.T) {
	t.Parallel()
	rs := Parse(`## [Unreleased]

## [0.2.0] - 2026-12-01

## [0.2.0-rc.2] - 2026-11-20

## [0.2.0-rc.1] - 2026-11-15

## [0.1.0] - 2026-11-09

## [0.1.0-beta.11] - 2026-11-01

## [0.1.0-beta.2] - 2026-10-20
`)
	for _, c := range []struct{ since, want string }{
		{"0.2.0", "Unreleased"},
		{"0.2.0-rc.1", "Unreleased,0.2.0,0.2.0-rc.2"},
		{"v0.1.0", "Unreleased,0.2.0,0.2.0-rc.2,0.2.0-rc.1"},
		{"0.1.0-beta.11", "Unreleased,0.2.0,0.2.0-rc.2,0.2.0-rc.1,0.1.0"},
		{"0.1.0-beta.2", "Unreleased,0.2.0,0.2.0-rc.2,0.2.0-rc.1,0.1.0,0.1.0-beta.11"},
	} {
		t.Run(c.since, func(t *testing.T) {
			t.Parallel()
			got, err := Since(rs, c.since)
			if err != nil || versions(got) != c.want {
				t.Errorf("since %q = %s (%v), want %s", c.since, versions(got), err, c.want)
			}
		})
	}
	if _, err := Since(rs, "0.1.0-"); !errors.Is(err, ErrBadSince) {
		t.Errorf("since 0.1.0-: err = %v, want ErrBadSince", err)
	}
}

func TestRenderRoundTrips(t *testing.T) {
	t.Parallel()
	rs := Parse(sample)
	again := Parse(Render(rs))
	if versions(again) != versions(rs) {
		t.Errorf("render/parse changed releases: %s vs %s", versions(again), versions(rs))
	}
	for i := range rs {
		if again[i].Body != rs[i].Body {
			t.Errorf("body %s changed:\n%q\n%q", rs[i].Version, rs[i].Body, again[i].Body)
		}
	}
}

// The embedded changelog must parse and must always have an Unreleased or a
// dated release at the top — otherwise `saladcloud changelog` is useless.
func TestEmbeddedChangelogParses(t *testing.T) {
	t.Parallel()
	rs := Parse(saladcloudcli.Changelog)
	if len(rs) == 0 {
		t.Fatal("embedded CHANGELOG.md has no release headings")
	}
	if !rs[0].Unreleased() && rs[0].Date == nil {
		t.Errorf("top release %q has no date", rs[0].Version)
	}
}
