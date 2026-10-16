package changelog

import (
	"errors"
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
	rs := Parse(sample)
	if len(rs) != 4 {
		t.Fatalf("got %d releases, want 4: %+v", len(rs), rs)
	}
	if rs[0].Version != "Unreleased" || rs[0].Date != nil || rs[0].Title == nil || *rs[0].Title != "first release" {
		t.Errorf("unreleased = %+v", rs[0])
	}
	if !strings.Contains(rs[0].Body, "unreleased thing") || strings.Contains(rs[0].Body, "one two") {
		t.Errorf("unreleased body wrong: %q", rs[0].Body)
	}
	if rs[1].Version != "1.2.0" || rs[1].Date == nil || *rs[1].Date != "2026-10-01" || rs[1].Title != nil {
		t.Errorf("1.2.0 = %+v", rs[1])
	}
	if rs[2].Title == nil || *rs[2].Title != "the middle one" {
		t.Errorf("1.1.0 title = %v", rs[2].Title)
	}
	if rs[3].Body != "- one oh" {
		t.Errorf("1.0.0 body = %q", rs[3].Body)
	}
}

func TestParseCRLF(t *testing.T) {
	rs := Parse(strings.ReplaceAll(sample, "\n", "\r\n"))
	if len(rs) != 4 || strings.Contains(rs[3].Body, "\r") {
		t.Errorf("CRLF parse: %+v", rs)
	}
}

func versions(rs []Release) string {
	var v []string
	for _, r := range rs {
		v = append(v, r.Version)
	}
	return strings.Join(v, ",")
}

func TestSince(t *testing.T) {
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

func TestRenderRoundTrips(t *testing.T) {
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
	rs := Parse(saladcloudcli.Changelog)
	if len(rs) == 0 {
		t.Fatal("embedded CHANGELOG.md has no release headings")
	}
	if !rs[0].Unreleased() && rs[0].Date == nil {
		t.Errorf("top release %q has no date", rs[0].Version)
	}
}
