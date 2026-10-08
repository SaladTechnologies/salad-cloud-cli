package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// describeNotFound rewrites a bare CodeNotFound error with the identity of
// the resource the command asked for, and hint (when not empty) as its fix.
// API-provided detail is kept when it says more than "Not Found" (and its
// problem type). When the project in use is the built-in fallback, the
// warning printed before the request already says so.
func describeNotFound(cerr *clierr.Error, hint, format string, args ...any) *clierr.Error {
	if cerr == nil || cerr.Code != clierr.CodeNotFound || cerr.IsFinal() {
		return cerr
	}
	context := fmt.Sprintf(format, args...)
	out := *cerr
	if isBareNotFound(out.Message) {
		out.Message = context
	} else {
		out.Message = fmt.Sprintf("%s (%s)", out.Message, context)
	}
	if hint != "" {
		out.Hint = &hint
	}
	return &out
}

// groupNotFound rewrites a 404 on a group-level call: the group, where it
// was looked for, and the command that lists what is there.
func groupNotFound(cerr *clierr.Error, group, org, project string) *clierr.Error {
	return describeNotFound(cerr, "to list the container groups, run saladcloud container-groups list",
		`container group "%s" not found in %s/%s`, group, org, project)
}

// problemTypeSuffix is the " (type: ...)" the API's problem documents add
// to a bare title.
var problemTypeSuffix = regexp.MustCompile(`\s*\(type: [^)]*\)$`)

// isBareNotFound reports whether a 404's message carries no information
// beyond the status itself — the API's framework default is a problem
// document whose title is just "Not Found", and empty or HTML bodies
// render as bare "HTTP 404" variants.
func isBareNotFound(msg string) bool {
	switch strings.ToLower(strings.TrimSpace(problemTypeSuffix.ReplaceAllString(msg, ""))) {
	case "", "not found", "404", "404 not found", "http 404", "http 404 (non-json error response)":
		return true
	}
	return false
}
