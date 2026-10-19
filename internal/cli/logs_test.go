package cli

import (
	"errors"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/logsq"
)

func TestNoMatchNote(t *testing.T) {
	tests := []struct {
		name                             string
		scope                            logScope
		since                            string
		instance, severity, filter, want string
	}{
		{
			name:  "bare",
			scope: logScope{kind: scopeGroup, name: "my-cg"}, since: "15m",
			want: `no log entries matched (group "my-cg", since 15m)`,
		},
		{
			name:     "all filters",
			scope:    logScope{kind: scopeGroup, name: "my-cg"},
			since:    "2h",
			instance: "8a2f13ba",
			severity: "warning",
			filter:   "timeout",
			want: `no log entries matched (group "my-cg", since 2h, instance "8a2f13ba", severity "warning", filter ` +
				`"timeout")`,
		},
		{
			name:  "filter only",
			scope: logScope{kind: scopeGroup, name: "api"}, since: "2026-01-02T15:04:05Z",
			filter: "panic",
			want:   `no log entries matched (group "api", since 2026-01-02T15:04:05Z, filter "panic")`,
		},
		{
			name:  "project scope",
			scope: logScope{kind: scopeProject, name: "prod"}, since: "1d",
			want: `no log entries matched (project "prod", since 1d)`,
		},
		{
			name:  "organization scope",
			scope: logScope{kind: scopeOrg, name: "acme"}, since: "15m",
			want: `no log entries matched (organization "acme", since 15m)`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := noMatchNote(tt.scope, tt.since, tt.instance, tt.severity, tt.filter)
			if got != tt.want {
				t.Errorf("noMatchNote = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFormatLogLineNamesItsSource: a query wider than one group must say
// which project and group each line came from; a group query stays as it was.
func TestFormatLogLineNamesItsSource(t *testing.T) {
	e := logsq.Entry{
		Time: "2026-10-01T12:00:00Z", Severity: "info", Text: "hello",
		Project: "prod", Group: "api", InstanceID: "8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37",
	}
	tests := []struct{ scope, want string }{
		{scopeGroup, "2026-10-01 12:00:00Z  INFO     [8a2f13ba] hello"},
		{scopeProject, "2026-10-01 12:00:00Z  INFO     [api/8a2f13ba] hello"},
		{scopeOrg, "2026-10-01 12:00:00Z  INFO     [prod/api/8a2f13ba] hello"},
	}
	for _, tt := range tests {
		if got := formatLogLine(e, tt.scope); got != tt.want {
			t.Errorf("formatLogLine(%s) = %q, want %q", tt.scope, got, tt.want)
		}
	}
	// A system event has no instance; the missing parts render as "-".
	sys := logsq.Entry{Time: "2026-10-01T12:00:00Z", Severity: "info", Text: "Instance Running", Project: "prod"}
	if got, want := formatLogLine(
		sys,
		scopeOrg,
	), "2026-10-01 12:00:00Z  INFO     [prod/-/-] Instance Running"; got != want {
		t.Errorf("system event line = %q, want %q", got, want)
	}
}

// TestLogsInstancePrefixNeedsGroup: an instance ID prefix can only be
// resolved against one group's instances, so without a group it is a usage
// error before any client or API work.
func TestLogsInstancePrefixNeedsGroup(t *testing.T) {
	app := newTestRun(t)
	_, err := app.runRoot(t, "logs", "--instance", "8a2f")
	var cerr *clierr.Error
	ok := errors.As(err, &cerr)
	if !ok || cerr.Code != clierr.CodeUsage {
		t.Fatalf("logs --instance <prefix> without a group = %v, want a usage error", err)
	}
}

// TestFormatLogLineReadsSystemEvents: a JSON entry with a message reads as
// the message and its non-empty fields, not as raw JSON; seconds are enough.
func TestFormatLogLineReadsSystemEvents(t *testing.T) {
	e := logsq.Entry{
		Time: "2026-10-03T23:06:26.8559213+00:00", Severity: "info", InstanceID: "bd0f69f5-0000",
		JSON: map[string]any{"gpu_class_name": "", "message": "Instance Allocated"},
	}
	if got, want := formatLogLine(
		e,
		scopeGroup,
	), "2026-10-03 23:06:26Z  INFO     [bd0f69f5] Instance Allocated"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
	if got, want := jsonLogText(
		map[string]any{"message": "done", "code": 3.0, "who": "w"},
	), "done code=3 who=w"; got != want {
		t.Errorf("jsonLogText = %q, want %q", got, want)
	}
	if got, want := jsonLogText(map[string]any{"status": 500.0}), `{"status":500}`; got != want {
		t.Errorf("jsonLogText without a message = %q, want %q", got, want)
	}
}

func TestTimeCell(t *testing.T) {
	for in, want := range map[string]string{
		"2026-10-03T14:37:17.7506105+00:00": "2026-10-03 14:37:17Z",
		"2026-10-03T16:37:17+02:00":         "2026-10-03 14:37:17Z",
		"not a time":                        "not a time",
	} {
		if got := timeCell(&in); got != want {
			t.Errorf("timeCell(%q) = %q, want %q", in, got, want)
		}
	}
	if timeCell(nil) != "-" {
		t.Error("timeCell(nil) is not -")
	}
}

// TestLogsScopeLine: table mode says what a logs query covers, because the
// organization-wide default differs from every other command.
func TestLogsScopeLine(t *testing.T) {
	for _, tc := range []struct {
		scope logScope
		want  string
	}{
		{logScope{scopeOrg, "o"}, "scope: organization o, all projects (--project or -c narrows it)"},
		{logScope{scopeProject, "p"}, "scope: project p in organization o"},
		{logScope{scopeGroup, "g"}, "scope: container group g in o/p"},
	} {
		if got := scopeLine(tc.scope, "o", "p"); got != tc.want {
			t.Errorf("scopeLine = %q, want %q", got, tc.want)
		}
	}
}
