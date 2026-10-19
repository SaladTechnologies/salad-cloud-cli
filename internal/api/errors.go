package api

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// codeForStatus maps an HTTP status to the CLI's exit code and error kind.
func codeForStatus(status int) (code int, kind clierr.Kind) {
	switch status {
	case 401, 403:
		return clierr.CodeAuth, clierr.KindAuth
	case 404:
		return clierr.CodeNotFound, clierr.KindNotFound
	case 429:
		return clierr.CodeRateLimited, clierr.KindRateLimited
	case 400, 422:
		return clierr.CodeInvalidInput, clierr.KindInvalidInput
	default:
		// Other 4xx and all 5xx.
		return clierr.CodeGeneric, clierr.KindAPIError
	}
}

// problemDoc matches the API's RFC7807-ish problem documents. Errors is
// kept raw because ASP.NET ValidationProblemDetails puts a field->messages
// dict there, but other shapes have been observed; a mismatch must not
// break decoding of the rest of the document.
type problemDoc struct {
	Type    string          `json:"type"`
	Title   string          `json:"title"`
	Detail  string          `json:"detail"`
	Message string          `json:"message"`
	Errors  json.RawMessage `json:"errors"`
}

// maxErrorMessageLen caps the flattened validation message; anything longer
// is truncated with "...".
const maxErrorMessageLen = 800

// flattenFieldErrors renders an ASP.NET ValidationProblemDetails errors
// dict ({"Name": ["msg", ...], ...}) as "Name: msg; Name: msg2; Other: msg".
// Fields are sorted alphabetically for deterministic output. Returns ""
// when the raw value is absent, not a field->messages dict, or empty.
func flattenFieldErrors(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var errs map[string][]string
	if err := json.Unmarshal(raw, &errs); err != nil || len(errs) == 0 {
		return ""
	}
	fields := make([]string, 0, len(errs))
	for f := range errs {
		fields = append(fields, f)
	}
	slices.Sort(fields)
	var parts []string
	for _, f := range fields {
		for _, m := range errs[f] {
			parts = append(parts, f+": "+m)
		}
	}
	return strings.Join(parts, "; ")
}

// truncateMessage caps s at limit bytes without splitting a UTF-8 rune,
// appending "..." when it truncates.
func truncateMessage(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// isRuneStart reports whether b can begin a UTF-8 encoded rune.
func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// errorFromBody is the error for an API response that failed: the code and
// kind for its status, the message from its body, and the fix when the
// CLI knows one.
func errorFromBody(status int, body []byte) *clierr.Error {
	code, kind := codeForStatus(status)
	msg, hint := messageFromBody(status, body)
	e := clierr.HTTP(code, kind, status, msg)
	if hint != "" {
		e = e.WithHint("%s", hint)
	}
	return e
}

// messageFromBody extracts a human message from an error response body
// (RFC7807-ish problem documents, with HTML and plain-text fallbacks: the
// API is known to return HTML error documents sometimes), and the fix for
// the problem types the CLI knows.
func messageFromBody(status int, body []byte) (msg, hint string) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return fmt.Sprintf("HTTP %d", status), ""
	}
	if strings.HasPrefix(trimmed, "<") {
		return fmt.Sprintf("HTTP %d (non-JSON error response)", status), ""
	}
	var doc problemDoc
	if err := json.Unmarshal(body, &doc); err == nil {
		if msg, hint := doc.message(status); msg != "" {
			return msg, hint
		}
	}
	if len(trimmed) > 200 {
		trimmed = trimmed[:200]
	}
	return fmt.Sprintf("HTTP %d: %s", status, trimmed), ""
}

// message is the problem document's message, with its domain type when that carries signal, and the fix for the
// problem types the CLI knows; the message is "" when the document says nothing the CLI can show.
func (doc *problemDoc) message(status int) (string, string) {
	msg, hint := cmp.Or(doc.Detail, doc.Title, doc.Message), ""
	// ASP.NET ValidationProblemDetails carries the actual field-level
	// messages in an errors dict; without them "One or more validation
	// errors occurred." says nothing actionable.
	flat := flattenFieldErrors(doc.Errors)
	if flat != "" {
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", status)
		}
		msg = truncateMessage(msg+"; "+flat, maxErrorMessageLen)
	}
	// Some API 400s carry only a domain type ("Bad Request (type: x)",
	// or "Entitlement check failed.") with no actionable detail; translate
	// the known ones into sentences. A real detail or field-error list
	// always wins; the known type's fix applies either way.
	if known, ok := typeHints()[doc.Type]; ok {
		if doc.Detail == "" && flat == "" {
			msg = known.msg
		}
		hint = known.hint
	}
	if msg != "" && doc.Type != "" && !isRFCTypeURL(doc.Type) {
		msg = fmt.Sprintf("%s (type: %s)", msg, doc.Type)
	}
	return msg, hint
}

// typeHints maps the API's opaque problem-document types to a sentence and
// the fix (the payload is just "Bad Request", or "Entitlement check
// failed.", plus the type). All three replica-quota variants (create,
// start, and update/scale) are distinct types.
func typeHints() map[string]struct{ msg, hint string } {
	return map[string]struct{ msg, hint string }{
		"no_credits_available": {
			"the organization is out of credits",
			"add credits at https://portal.salad.com; creating, starting or scaling workloads, inference and transcription all need them",
		},
		"created_replicas_quota_exceeded": {
			"the requested replica count exceeds the organization's replica quota",
			"to see the quota, run saladcloud quotas get",
		},
		"replicas_quota_exceeded": {
			"cannot start the group: its replica count exceeds the organization's replica quota",
			"to see the quota, run saladcloud quotas get",
		},
		"updated_replicas_quota_exceeded": {
			"the new replica count exceeds the organization's replica quota",
			"to see the quota, run saladcloud quotas get",
		},
		"name_conflict": {
			"a container group with this name already exists in this project, or existed and was deleted",
			"pick a different name; names are unique within the organization and project, and deleting a group does not release its name",
		},
		"queue_name_conflict": {
			"a queue with this name already exists in this project",
			"pick a different name, or delete that queue first; unlike a container group's, a deleted queue's name can be used again",
		},
		"pending_update_in_progress": {
			"the group has a change still being applied (a new group's first deploy, or an earlier update)",
			"wait for pending_change to clear and try again, which can take a long time under platform load; to watch it, run saladcloud container-groups get <name>",
		},
	}
}

// isRFCTypeURL reports whether a problem document's type is a generic
// RFC-reference URL, the framework default on plain errors (observed
// live: "https://tools.ietf.org/html/rfc9110#section-15.5.5" on every
// 404). Those add noise, so the "(type: ...)" hint is suppressed for
// them. Short domain-specific types (e.g. "no_credits_available") and
// non-RFC URLs are kept — they carry real signal.
func isRFCTypeURL(t string) bool {
	lower := strings.ToLower(t)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return false
	}
	return strings.Contains(lower, "tools.ietf.org") || strings.Contains(lower, "rfc")
}
