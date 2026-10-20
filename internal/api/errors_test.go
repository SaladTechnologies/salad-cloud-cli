package api

import (
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

func TestCodeForStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status   int
		wantCode int
		wantKind clierr.Kind
	}{
		{401, clierr.CodeAuth, "auth"},
		{403, clierr.CodeAuth, "auth"},
		{404, clierr.CodeNotFound, "not_found"},
		{429, clierr.CodeRateLimited, "rate_limited"},
		{400, clierr.CodeInvalidInput, "invalid_input"},
		{422, clierr.CodeInvalidInput, "invalid_input"},
		{418, clierr.CodeGeneric, "api_error"}, // other 4xx
		{500, clierr.CodeGeneric, "api_error"},
		{502, clierr.CodeGeneric, "api_error"},
	}
	for _, tt := range tests {
		code, kind := codeForStatus(tt.status)
		if code != tt.wantCode || kind != tt.wantKind {
			t.Errorf("codeForStatus(%d) = (%d, %q), want (%d, %q)",
				tt.status, code, kind, tt.wantCode, tt.wantKind)
		}
	}
}

func TestMessageFromBody(t *testing.T) {
	t.Parallel()
	htmlPage := "<!DOCTYPE html>\n<html><head><title>502 Bad Gateway</title></head><body>bad gateway</body></html>"

	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "HTML error page",
			status: 502,
			body:   htmlPage,
			want:   "HTTP 502 (non-JSON error response)",
		},
		{
			name:   "HTML fragment",
			status: 429,
			body:   "<html><body>Too Many Requests</body></html>",
			want:   "HTTP 429 (non-JSON error response)",
		},
		{
			name:   "domain-specific type hint is kept",
			status: 400,
			body:   `{"type":"no_credits_available","title":"No credits","detail":"The organization has no credits"}`,
			want:   "The organization has no credits (type: no_credits_available)",
		},
		{
			name:   "non-RFC URL type hint is kept",
			status: 400,
			body: `{"type":"https://example.com/probs/invalid-name","title":"Invalid name","detail":"Container group ` +
				`names must be lowercase"}`,
			want: "Container group names must be lowercase (type: https://example.com/probs/invalid-name)",
		},
		{
			name:   "tools.ietf.org type hint is suppressed (live 404 default)",
			status: 404,
			body:   `{"type":"https://tools.ietf.org/html/rfc9110#section-15.5.5","title":"Not Found"}`,
			want:   "Not Found",
		},
		{
			name:   "http (not https) ietf type hint is suppressed",
			status: 400,
			body:   `{"type":"http://tools.ietf.org/html/rfc7231#section-6.5.1","title":"Bad Request"}`,
			want:   "Bad Request",
		},
		{
			name:   "rfc-containing URL type hint is suppressed",
			status: 404,
			body:   `{"type":"https://www.rfc-editor.org/rfc/rfc9110#name-404-not-found","title":"Not Found"}`,
			want:   "Not Found",
		},
		{
			name:   "bare rfc word without URL prefix is kept",
			status: 400,
			body:   `{"type":"rfc_validation_failed","title":"Bad Request"}`,
			want:   "Bad Request (type: rfc_validation_failed)",
		},
		{
			name:   "known type: quota exceeded gets a sentence",
			status: 400,
			body:   `{"type":"created_replicas_quota_exceeded","title":"Bad Request"}`,
			want: "the requested replica count exceeds the organization's replica quota (type: " +
				"created_replicas_quota_exceeded)",
		},
		{
			// Live body captured from an organization with no credits.
			name:   "known type: no credits gets a portal remediation sentence",
			status: 400,
			body: `{"type":"no_credits_available","title":"Entitlement check ` +
				`failed.","status":400,"instance":"54976218"}`,
			want: "the organization is out of credits (type: no_credits_available)",
		},
		{
			// Live body captured from a start under zeroed quota.
			name:   "known type: start-time quota exceeded gets a sentence",
			status: 400,
			body:   `{"type":"replicas_quota_exceeded","title":"Bad Request"}`,
			want: "cannot start the group: its replica count exceeds the organization's replica quota (type: " +
				"replicas_quota_exceeded)",
		},
		{
			// Live body captured from a scale under zeroed quota.
			name:   "known type: update/scale quota exceeded gets a sentence",
			status: 400,
			body:   `{"type":"updated_replicas_quota_exceeded","title":"Bad Request"}`,
			want: "the new replica count exceeds the organization's replica quota (type: " +
				"updated_replicas_quota_exceeded)",
		},
		{
			name:   "known type: name conflict gets a sentence",
			status: 400,
			body:   `{"type":"name_conflict","title":"Bad Request"}`,
			want: "a container group with this name already exists in this project, or existed and was deleted " +
				"(type: name_conflict)",
		},
		{
			// The API's body for a duplicate queues create.
			name:   "known type: queue name conflict gets a sentence",
			status: 400,
			body:   `{"type":"queue_name_conflict","title":"Bad Request"}`,
			want:   "a queue with this name already exists in this project (type: queue_name_conflict)",
		},
		{
			name:   "known type: pending update in progress gets a sentence",
			status: 400,
			body:   `{"type":"pending_update_in_progress","title":"Bad Request"}`,
			want: "the group has a change still being applied (a new group's first deploy, or an earlier update) " +
				"(type: pending_update_in_progress)",
		},
		{
			name:   "title only",
			status: 404,
			body:   `{"title":"Not Found"}`,
			want:   "Not Found",
		},
		{
			name:   "message field fallback",
			status: 422,
			body:   `{"message":"replica count out of range"}`,
			want:   "replica count out of range",
		},
		{
			name:   "detail wins over title and message",
			status: 400,
			body:   `{"title":"Bad Request","detail":"the detail","message":"the message"}`,
			want:   "the detail",
		},
		{
			name:   "empty body",
			status: 500,
			body:   "",
			want:   "HTTP 500",
		},
		{
			name:   "whitespace-only body",
			status: 500,
			body:   "  \n\t ",
			want:   "HTTP 500",
		},
		{
			name:   "non-JSON non-HTML body falls back to snippet",
			status: 503,
			body:   "service unavailable",
			want:   "HTTP 503: service unavailable",
		},
		{
			name:   "JSON without any message fields falls back to snippet",
			status: 500,
			body:   `{"unrelated":true}`,
			want:   `HTTP 500: {"unrelated":true}`,
		},
		{
			name:   "long body truncated to 200 chars",
			status: 500,
			body:   strings.Repeat("x", 500),
			want:   "HTTP 500: " + strings.Repeat("x", 200),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := messageFromBody(tt.status, []byte(tt.body)).msg; got != tt.want {
				t.Errorf("messageFromBody(%d, %q) = %q, want %q", tt.status, tt.body, got, tt.want)
			}
		})
	}
}

func TestMessageFromBodyValidationErrors(t *testing.T) {
	t.Parallel()
	// The exact payload observed live from `create` with a bad name.
	livePayload := `{"type":"https://tools.ietf.org/html/rfc9110#section-15.5.1","title":"One or more validation ` +
		`errors occurred.","status":400,"errors":{"Name":["The field Name must match the regular expression ` +
		`'^[a-z][a-z0-9-]{0,61}[a-z0-9]$'.","The field Name must be a string with a minimum length of 2 and a ` +
		`maximum length of 63."]},"traceId":"00-abc-def-00"}`

	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "live ASP.NET ValidationProblemDetails payload",
			status: 400,
			body:   livePayload,
			want: "One or more validation errors occurred.; Name: The field Name must match the regular expression " +
				"'^[a-z][a-z0-9-]{0,61}[a-z0-9]$'.; Name: The field Name must be a string with a minimum length of 2 " +
				"and a maximum length of 63.",
		},
		{
			name:   "multiple fields sorted alphabetically",
			status: 400,
			body: `{"title":"One or more validation errors occurred.","errors":{"Replicas":["out of ` +
				`range"],"Name":["too short"]}}`,
			want: "One or more validation errors occurred.; Name: too short; Replicas: out of range",
		},
		{
			name:   "empty errors dict leaves the message unchanged",
			status: 400,
			body:   `{"title":"One or more validation errors occurred.","errors":{}}`,
			want:   "One or more validation errors occurred.",
		},
		{
			name:   "detail present still gets the errors appended",
			status: 400,
			body:   `{"title":"the title","detail":"the detail","errors":{"Name":["too short"]}}`,
			want:   "the detail; Name: too short",
		},
		{
			name:   "errors of an unexpected shape are ignored",
			status: 400,
			body:   `{"title":"Bad Request","errors":"boom"}`,
			want:   "Bad Request",
		},
		{
			name:   "errors dict without a title still surfaces the fields",
			status: 400,
			body:   `{"errors":{"Name":["too short"]}}`,
			want:   "HTTP 400; Name: too short",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := messageFromBody(tt.status, []byte(tt.body)).msg; got != tt.want {
				t.Errorf("messageFromBody(%d, %q) = %q, want %q", tt.status, tt.body, got, tt.want)
			}
		})
	}
}

func TestMessageFromBodyValidationErrorsCapped(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("y", 2000)
	body := `{"title":"One or more validation errors occurred.","errors":{"Name":["` + long + `"]}}`
	got := messageFromBody(400, []byte(body)).msg
	if len(got) > maxErrorMessageLen+len("...") {
		t.Errorf("message length = %d, want <= %d", len(got), maxErrorMessageLen+len("..."))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated message %q does not end with …", got)
	}
	if !strings.HasPrefix(got, "One or more validation errors occurred.; Name: yyy") {
		t.Errorf("truncated message %q lost its prefix", got)
	}
}

// TestKnownProblemTypesCarryTheirFix: a known problem type's fix is the
// error's hint, whether or not the API sent a detail.
func TestKnownProblemTypesCarryTheirFix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ body, hint string }{
		{`{"type":"created_replicas_quota_exceeded","title":"Bad ` +
			`Request"}`, "to see the quota, run saladcloud quotas get"},
		{`{"type":"name_conflict","title":"Bad Request"}`, "pick a different name; names are unique within the " +
			"organization and project, and deleting a group does not release its name"},
		{`{"type":"pending_update_in_progress","detail":"busy"}`, "wait for pending_change to clear and try again, " +
			"which can take a long time under platform load; to watch it, run saladcloud container-groups get <name>"},
		{`{"type":"no_credits_available","title":"Entitlement check ` +
			`failed."}`, "add credits at https://portal.salad.com; " +
			"creating, starting or scaling workloads, inference and transcription all need them"},
	} {
		e := errorFromBody(400, []byte(tc.body))
		if e.HintText() != tc.hint || e.Code != clierr.CodeInvalidInput {
			t.Errorf("%s: %+v hint %q", tc.body, e, e.HintText())
		}
	}
	if e := errorFromBody(400, []byte(`{"title":"Bad Request"}`)); e.Hint != nil {
		t.Errorf("an unknown problem got a hint: %q", e.HintText())
	}
}
