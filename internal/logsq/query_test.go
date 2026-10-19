package logsq

import (
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

func TestCompile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		f    Filters
		want string
	}{
		{
			name: "group only",
			f:    Filters{Group: "my-cg"},
			want: `resource.labels.container_group_name = "my-cg"`,
		},
		{
			name: "group and project",
			f:    Filters{Group: "my-cg", Project: "prod"},
			want: `resource.labels.container_group_name = "my-cg" and resource.labels.project_name = "prod"`,
		},
		{
			name: "instance",
			f:    Filters{Group: "g", Instance: "8a2f13ba-01c2"},
			want: `resource.labels.container_group_name = "g" and resource.labels.instance_id = "8a2f13ba-01c2"`,
		},
		{
			name: "severity",
			f:    Filters{Group: "g", MinSeverity: "warning"},
			want: `resource.labels.container_group_name = "g" and severity >= "warning"`,
		},
		{
			name: "severity is case-normalized",
			f:    Filters{Group: "g", MinSeverity: "Warning"},
			want: `resource.labels.container_group_name = "g" and severity >= "warning"`,
		},
		{
			name: "contains",
			f:    Filters{Group: "g", Contains: "timeout"},
			want: `resource.labels.container_group_name = "g" and log contains "timeout"`,
		},
		{
			name: "raw query wrapped in parens",
			f:    Filters{Group: "g", RawQuery: `json_log.status >= 500 or text_log startswith "panic"`},
			want: `resource.labels.container_group_name = "g" and (json_log.status >= 500 or text_log startswith ` +
				`"panic")`,
		},
		{
			name: "everything, in order, raw last",
			f: Filters{
				Group: "g", Project: "p", Instance: "i", MinSeverity: "error",
				Contains: "boom", RawQuery: `resource.type = "container"`,
			},
			want: `resource.labels.container_group_name = "g" and resource.labels.project_name = "p" and ` +
				`resource.labels.instance_id = "i" and severity >= "error" and log contains "boom" and ` +
				`(resource.type = "container")`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Compile(tt.f)
			if err != nil {
				t.Fatalf("Compile(%+v) error: %v", tt.f, err)
			}
			if got != tt.want {
				t.Errorf("Compile mismatch\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

func TestCompileEscaping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string // expected quoted literal for the group clause
	}{
		{"plain", `my-cg`, `"my-cg"`},
		{"embedded quote", `my"cg`, `"my\"cg"`},
		{"embedded backslash", `a\b`, `"a\\b"`},
		{"backslash then quote", `a\"b`, `"a\\\"b"`},
		{"quote then backslash", `a"\b`, `"a\"\\b"`},
		{"double backslash", `a\\b`, `"a\\\\b"`},
		{"trailing backslash", `a\`, `"a\\"`},
		{"only quotes", `"""`, `"\"\"\""`},
		{"injection attempt", `x" or severity >= "default`, `"x\" or severity >= \"default"`},
		{"sql-ish injection", `"; drop`, `"\"; drop"`},
		{"unicode untouched", `héllo→世界`, `"héllo→世界"`}, //nolint:gosmopolitan // non-Latin text on purpose
		{"newline kept as-is", "a\nb", "\"a\nb\""},
		{"tab kept as-is", "a\tb", "\"a\tb\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Compile(Filters{Group: tt.in})
			if err != nil {
				t.Fatalf("Compile error: %v", err)
			}
			want := "resource.labels.container_group_name = " + tt.want
			if got != want {
				t.Errorf("escaping mismatch\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

func TestCompileEscapingAppliesToAllQuotedFields(t *testing.T) {
	t.Parallel()
	got, err := Compile(Filters{
		Group:    `g"1`,
		Project:  `p\2`,
		Instance: `i"3`,
		Contains: `say "hi"`,
	})
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	want := `resource.labels.container_group_name = "g\"1" and resource.labels.project_name = "p\\2" and ` +
		`resource.labels.instance_id = "i\"3" and log contains "say \"hi\""`
	if got != want {
		t.Errorf("mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestCompileRawQueryNotEscaped(t *testing.T) {
	t.Parallel()
	raw := `text_log contains "a\"b" and json_log.n > 3`
	got, err := Compile(Filters{Group: "g", RawQuery: raw})
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	if !strings.HasSuffix(got, " and ("+raw+")") {
		t.Errorf("raw query was not passed through verbatim: %s", got)
	}
}

func TestCompileAllSeveritiesAccepted(t *testing.T) {
	t.Parallel()
	for _, sev := range Severities() {
		if _, err := Compile(Filters{Group: "g", MinSeverity: sev}); err != nil {
			t.Errorf("severity %q rejected: %v", sev, err)
		}
	}
}

func TestCompileInvalidSeverity(t *testing.T) {
	t.Parallel()
	_, err := Compile(Filters{Group: "g", MinSeverity: "bogus"})
	if err == nil {
		t.Fatal("expected an error for invalid severity")
	}
	if err.Code != clierr.CodeInvalidInput {
		t.Errorf("code = %d, want %d", err.Code, clierr.CodeInvalidInput)
	}
	for _, sev := range Severities() {
		if !strings.Contains(err.Message+" "+err.HintText(), sev) {
			t.Errorf("error message should list valid severity %q: %s", sev, err.Message)
		}
	}
	if !strings.Contains(err.Message, "bogus") {
		t.Errorf("error message should name the bad value: %s", err.Message)
	}
}

func TestCompileNeverEmitsReceiveTime(t *testing.T) {
	t.Parallel()
	// receive_time is not a queryable field (the API rejects it with
	// invalid_query_syntax), so no filter combination may compile it in.
	got, err := Compile(Filters{
		Group: "g", Project: "p", Instance: "i", MinSeverity: "error", Contains: "boom",
	})
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	if strings.Contains(got, "receive_time") {
		t.Errorf("compiled query must never reference receive_time: %s", got)
	}
}

func TestCompileNoScope(t *testing.T) {
	t.Parallel()
	_, err := Compile(Filters{MinSeverity: "error"})
	if err == nil {
		t.Fatal("expected an error with no organization, project, or group")
	}
	if err.Code != clierr.CodeUsage {
		t.Errorf("code = %d, want %d", err.Code, clierr.CodeUsage)
	}
}

// TestCompileScopes: the narrowest scope given wins. The API rejects an
// empty query, so the organization scope is an explicit label match.
func TestCompileScopes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		f    Filters
		want string
	}{
		{"organization", Filters{Org: "acme"}, `resource.labels.organization_name = "acme"`},
		{"project", Filters{Org: "acme", Project: "prod"}, `resource.labels.project_name = "prod"`},
		{
			"group within a project",
			Filters{Org: "acme", Project: "prod", Group: "api"},
			`resource.labels.container_group_name = "api" and resource.labels.project_name = "prod"`,
		},
		{
			"organization with filters",
			Filters{Org: "acme", MinSeverity: "error", Contains: "boom"},
			`resource.labels.organization_name = "acme" and severity >= "error" and log contains "boom"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Compile(tt.f)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if got != tt.want {
				t.Errorf("Compile = %s, want %s", got, tt.want)
			}
		})
	}
}
