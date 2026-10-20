package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/logsq"
)

func TestNoMatchNote(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
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
	t.Parallel()
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
		if got := formatLogLine(&e, tt.scope); got != tt.want {
			t.Errorf("formatLogLine(%s) = %q, want %q", tt.scope, got, tt.want)
		}
	}
	// A system event has no instance; the missing parts render as "-".
	sys := logsq.Entry{Time: "2026-10-01T12:00:00Z", Severity: "info", Text: "Instance Running", Project: "prod"}
	got, want := formatLogLine(&sys, scopeOrg), "2026-10-01 12:00:00Z  INFO     [prod/-/-] Instance Running"
	if got != want {
		t.Errorf("system event line = %q, want %q", got, want)
	}
}

// TestLogsInstancePrefixNeedsGroup: an instance ID prefix can only be
// resolved against one group's instances, so without a group it is a usage
// error before any client or API work.
func TestLogsInstancePrefixNeedsGroup(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	_, err := app.runRoot(t, "logs", "--instance", "8a2f")
	var cerr *clierr.Error
	ok := errors.As(err, &cerr)
	if !ok || cerr.Code != clierr.CodeUsage {
		t.Fatalf("logs --instance <prefix> without a group = %v, want a usage error", err)
	}
}

// TestLogsLimitAndPollUsage: a negative --limit and a poll interval under the 5s minimum are usage errors before any
// client or API work.
func TestLogsLimitAndPollUsage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"logs", "--limit", "-1"}, "--limit must be 0 (all) or positive (got -1)"},
		{[]string{"logs", "-f", "--poll-interval", "3s"}, "--poll-interval must be at least 5s (got 3s)"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			_, err := newTestRun(t).runRoot(t, tc.args...)
			var cerr *clierr.Error
			if !errors.As(err, &cerr) || cerr.Code != clierr.CodeUsage || cerr.Message != tc.want {
				t.Fatalf("error = %v, want usage error %q", err, tc.want)
			}
		})
	}
}

// fakeLogAPI serves /log-entries from lines, emitted one second apart ending a minute ago, the way the API does:
// the entries in [start_time, end_time), sorted by sort_order, page_size at a time. It records each sort order asked
// for.
type fakeLogAPI struct {
	mu     sync.Mutex
	orders []string
}

func newFakeLogAPI(t *testing.T, app *testRun, lines int) *fakeLogAPI {
	t.Helper()
	f := &fakeLogAPI{mu: sync.Mutex{}, orders: nil}
	last := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	times := make([]time.Time, lines)
	for i := range times {
		times[i] = last.Add(time.Duration(i-lines+1) * time.Second)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/log-entries") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var q struct {
			Start    time.Time `json:"start_time"`
			End      time.Time `json:"end_time"`
			PageSize int       `json:"page_size"`
			Order    string    `json:"sort_order"`
		}
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			t.Errorf("decoding the log query: %v", err)
		}
		f.mu.Lock()
		f.orders = append(f.orders, q.Order)
		f.mu.Unlock()
		respond(t, w, logEntriesBody(times, q.Start, q.End, q.PageSize, q.Order))
	}))
	t.Cleanup(srv.Close)
	app.setenv("SALAD_CLI_API_BASE_URL", srv.URL)
	app.setenv("SALAD_API_KEY", "salad_cloud_user_test0000")
	app.setenv("SALAD_ORGANIZATION", "my-org")
	return f
}

// logEntriesBody is a log-entries response holding the entries of times in [start, end), in order, at most pageSize;
// each entry's text is its index in times.
func logEntriesBody(times []time.Time, start, end time.Time, pageSize int, order string) string {
	var items []string
	for i := range times {
		if times[i].Before(start) || !times[i].Before(end) {
			continue
		}
		ts := times[i].Format(time.RFC3339Nano)
		items = append(items, fmt.Sprintf(
			`{"time":%q,"receive_time":%q,"severity":"info","text_log":"line %d",`+
				`"resource":{"type":"container","labels":{}}}`,
			ts,
			ts,
			i,
		))
	}
	if order == string(logsq.Descending) {
		slices.Reverse(items)
	}
	return `{"items":[` + strings.Join(items[:min(len(items), pageSize)], ",") + `]}`
}

// printedLogLines is which of texts each line of out printed, in order: an NDJSON line holds its text quoted, a
// table line ends with it.
func printedLogLines(out string, texts []string) []string {
	var got []string
	for line := range strings.Lines(strings.TrimSpace(out)) {
		line = strings.TrimSpace(line)
		printed := func(s string) bool { return strings.Contains(line, `"`+s+`"`) || strings.HasSuffix(line, "] "+s) }
		if i := slices.IndexFunc(texts, printed); i >= 0 {
			got = append(got, texts[i])
		}
	}
	return got
}

// ordersAsked is every sort order the fake API was asked for.
func (f *fakeLogAPI) ordersAsked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.orders)
}

// TestLogsLimitKeepsTheNewest: by default and with --limit, logs reads newest first and prints the newest entries
// oldest first, with a note on stderr (in every mode, the hint in table mode only) when it left older ones out;
// --limit 0 reads the whole range oldest first.
func TestLogsLimitKeepsTheNewest(t *testing.T) {
	t.Parallel()
	all := []string{"line 0", "line 1", "line 2", "line 3", "line 4"}
	for _, tc := range []struct {
		name      string
		args      []string
		wantLines []string
		wantOrder string
		wantNote  bool
		wantHint  bool
	}{
		{"default limit", []string{"-o", "json"}, all, "desc", false, false},
		{"limit 2, json", []string{"--limit", "2", "-o", "json"}, all[3:], "desc", true, false},
		{"limit 2, table", []string{"--limit", "2"}, all[3:], "desc", true, true},
		{"limit 0", []string{"--limit", "0", "-o", "json"}, all, "asc", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app := newTestRun(t)
			api := newFakeLogAPI(t, app, len(all))
			var stderr bytes.Buffer
			app.Stderr = &stderr
			out, err := app.runRoot(t, append([]string{"logs", "--page-size", "2"}, tc.args...)...)
			if err != nil {
				t.Fatalf("logs %v: %v (stderr %s)", tc.args, err, stderr.String())
			}
			if got := printedLogLines(out, all); !slices.Equal(got, tc.wantLines) {
				t.Errorf("printed %v, want %v; stdout:\n%s", got, tc.wantLines, out)
			}
			if asked := api.ordersAsked(); slices.ContainsFunc(
				asked,
				func(o string) bool { return o != tc.wantOrder },
			) {
				t.Errorf("asked for sort_order %v, want only %q", asked, tc.wantOrder)
			}
			note := strings.Contains(stderr.String(), "note: showing the newest 2 log entries")
			hint := strings.Contains(stderr.String(), "hint: for more, raise --limit")
			if note != tc.wantNote || hint != tc.wantHint {
				t.Errorf(
					"stderr note %t, hint %t; want %t, %t; stderr:\n%s",
					note,
					hint,
					tc.wantNote,
					tc.wantHint,
					stderr.String(),
				)
			}
		})
	}
}

// TestFormatLogLineReadsSystemEvents: a JSON entry with a message reads as
// the message and its non-empty fields, not as raw JSON; seconds are enough.
func TestFormatLogLineReadsSystemEvents(t *testing.T) {
	t.Parallel()
	e := logsq.Entry{
		Time: "2026-10-03T23:06:26.8559213+00:00", Severity: "info", InstanceID: "bd0f69f5-0000",
		JSON: map[string]any{"gpu_class_name": "", "message": "Instance Allocated"},
	}
	got, want := formatLogLine(&e, scopeGroup), "2026-10-03 23:06:26Z  INFO     [bd0f69f5] Instance Allocated"
	if got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
	text := jsonLogText(map[string]any{"message": "done", "code": 3.0, "who": "w"})
	if want := "done code=3 who=w"; text != want {
		t.Errorf("jsonLogText = %q, want %q", text, want)
	}
	if got, want := jsonLogText(map[string]any{"status": 500.0}), `{"status":500}`; got != want {
		t.Errorf("jsonLogText without a message = %q, want %q", got, want)
	}
}

func TestTimeCell(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
