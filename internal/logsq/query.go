// Package logsq holds the pure logic behind the logs command: query
// compilation, time-reference parsing, ascending window pagination with
// boundary dedup (log entries have no unique IDs), and the follow loop.
// It deliberately imports no SDK packages so every piece is unit-testable
// with plain strings and fakes.
package logsq

import (
	"strings"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// Severities is the log query language's ordered severity enum, lowest
// first. The API treats severity as orderable, so `severity >= "warning"`
// matches warning and everything above it.
var Severities = []string{
	"default", "debug", "info", "notice", "warning",
	"error", "critical", "alert", "emergency",
}

// Filters describes the clauses Compile assembles into a log query. The
// scope is the narrowest of Group, Project, and Org that is set: a group
// (within Project when that is set too), a whole project, or the whole
// organization.
type Filters struct {
	Org         string // scope when Group and Project are empty: resource.labels.organization_name
	Project     string // optional: resource.labels.project_name; the scope when Group is empty
	Group       string // optional: resource.labels.container_group_name
	Instance    string // optional: resource.labels.instance_id
	MinSeverity string // optional: severity >= "<v>" (validated against Severities)
	Contains    string // optional: log contains "<v>" (full-text across text_log + json_log)
	RawQuery    string // optional: appended verbatim as `and (<raw>)`
}

// Note: the query accepts `time` only — receive_time is rejected with
// invalid_query_syntax — so no receive_time clause exists here; the follow
// loop dedups client-side instead (see follow.go).

// Compile assembles a log query string from f. A scope is required (Group,
// Project, or Org); every other filter is optional. The API rejects an
// empty query, so an organization-wide search is expressed as a match on
// the organization_name label, which every entry carries. All values
// except RawQuery are quoted and escaped; RawQuery is the user's own
// query-language syntax and passes through verbatim inside parentheses.
func Compile(f Filters) (string, *clierr.Error) {
	var b strings.Builder
	switch {
	case f.Group != "":
		b.WriteString("resource.labels.container_group_name = ")
		b.WriteString(quoteValue(f.Group))
		if f.Project != "" {
			b.WriteString(" and resource.labels.project_name = ")
			b.WriteString(quoteValue(f.Project))
		}
	case f.Project != "":
		b.WriteString("resource.labels.project_name = ")
		b.WriteString(quoteValue(f.Project))
	case f.Org != "":
		b.WriteString("resource.labels.organization_name = ")
		b.WriteString(quoteValue(f.Org))
	default:
		return "", clierr.New(clierr.CodeUsage, clierr.KindUsage, "an organization, project, or container group is required to query logs")
	}
	if f.Instance != "" {
		b.WriteString(" and resource.labels.instance_id = ")
		b.WriteString(quoteValue(f.Instance))
	}
	if f.MinSeverity != "" {
		sev := strings.ToLower(f.MinSeverity)
		if !validSeverity(sev) {
			return "", clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, `invalid severity "%s"`, f.MinSeverity).
				WithHint("use one of %s", strings.Join(Severities, ", "))
		}
		b.WriteString(" and severity >= ")
		b.WriteString(quoteValue(sev))
	}
	if f.Contains != "" {
		b.WriteString(" and log contains ")
		b.WriteString(quoteValue(f.Contains))
	}
	if f.RawQuery != "" {
		b.WriteString(" and (")
		b.WriteString(f.RawQuery)
		b.WriteString(")")
	}
	return b.String(), nil
}

func validSeverity(s string) bool {
	for _, v := range Severities {
		if s == v {
			return true
		}
	}
	return false
}

// quoteValue renders s as a double-quoted query-language string literal,
// escaping backslashes and double quotes. This is the injection surface:
// everything else (newlines, unicode, ...) stays as-is inside the quotes.
func quoteValue(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		default:
			b.WriteByte(s[i])
		}
	}
	b.WriteByte('"')
	return b.String()
}
