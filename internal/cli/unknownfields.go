// The --allow-unknown-fields escape hatch, shared by create and update.
//
// Strict rejection of unknown -f fields is the right default for typos, but
// it turns additive API evolution into CLI breakage: a field the API ships
// tomorrow is rejected until this CLI's pinned SDK model is regenerated.
// --allow-unknown-fields is the explicit acknowledgement — the user opts in
// to sending fields this build cannot vouch for, each one warned on stderr.
//
// A rejected field is not always a typo or a future field, though. It may be
// a perfectly real field that simply is not writable on this endpoint —
// networking.auth is create-only (the patch schema carries only port), and a
// document piped back from `get -o json` is full of response-only fields.
// Offering the escape hatch for those is actively misleading: the flag sends
// them, the API accepts them, nothing happens, and the caller gets exit 0
// believing the write landed. classifyUnknown separates the cases by asking
// the *other* SDK schemas where the field lives, so the advice is derived
// from the model rather than from a hand-maintained list of gotchas.

package cli

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/input"
)

// addAllowUnknownFlag registers --allow-unknown-fields on a -f command.
func addAllowUnknownFlag(cmd *cobra.Command) {
	cmd.Flags().Bool("allow-unknown-fields", false,
		"send -f fields this CLI's API model doesn't recognize instead of rejecting them (forward-compat escape hatch; each is warned on stderr)")
}

// unknownKind classifies why a field was rejected, which decides the advice.
type unknownKind int

const (
	// unknownNovel: no schema knows this field — a typo, or a field newer
	// than this build. The escape hatch is the right suggestion.
	unknownNovel unknownKind = iota
	// unknownCreateOnly: the create request accepts it, this write does not.
	unknownCreateOnly
	// unknownResponseOnly: only responses carry it, so it is not writable
	// anywhere (id, create_time, current_state, version …).
	unknownResponseOnly
	// unknownRelocated: writable, but requests spell its path differently
	// than responses do (responses return priority top-level; requests take
	// container.priority).
	unknownRelocated
	// unknownMisplaced: a real field nested at the wrong level, such as
	// readiness_probe under container instead of beside it.
	unknownMisplaced
)

// createOnlyReasons explains create-only fields whose reason is not obvious,
// keyed by the top-level field: the advice replaces the generic "recreate
// the group".
var createOnlyReasons = map[string]string{
	"queue_connection": "a group's queue connection is fixed when the group is created; to change it, run " +
		"saladcloud container-groups create <new-name> --from <group> --queue <queue> --queue-port <n> --queue-path <path>",
}

// createOnlyReason returns the specific advice for a create-only path, or "".
func createOnlyReason(path string) string {
	top, _, _ := strings.Cut(path, ".")
	return createOnlyReasons[top]
}

// maxListedPaths caps how many field paths one grouped clause names.
const maxListedPaths = 8

// classifyUnknown places one rejected path against the create-request and
// response schemas. requestPath is set for unknownRelocated and
// unknownMisplaced.
func classifyUnknown(path string) (kind unknownKind, requestPath string) {
	createT := reflect.TypeFor[containergroups.ContainerGroupCreationRequest]()
	responseT := reflect.TypeFor[shared.ContainerGroup]()

	if input.FieldPathKnown(createT, path) {
		return unknownCreateOnly, ""
	}
	if !input.FieldPathKnown(responseT, path) {
		if at := misplacedAt(createT, path); at != "" {
			return unknownMisplaced, at
		}
		return unknownNovel, ""
	}
	// A response field. If the same leaf name is writable elsewhere in the
	// create schema, the caller just used the response's spelling.
	leaf := path
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		leaf = path[i+1:]
	}
	if at := input.FindFieldPath(createT, leaf); at != "" && at != path {
		return unknownRelocated, at
	}
	return unknownResponseOnly, ""
}

// misplacedAt returns where a field put at the wrong one of the two levels a
// definition's fields live at belongs: container.X for a top-level X that is
// a container field, or X for a container.X that is a top-level field. Only
// those two swaps: a deeper path such as readiness_probe.command or
// queue_connection.name is a different mistake, and its leaf is no guide.
func misplacedAt(createT reflect.Type, path string) string {
	var at string
	switch {
	case !strings.ContainsAny(path, ".["):
		at = "container." + path
	case strings.HasPrefix(path, "container.") && !strings.ContainsAny(path[len("container."):], ".["):
		at = path[len("container."):]
	default:
		return ""
	}
	if input.FieldPathKnown(createT, at) {
		return at
	}
	return ""
}

// joinPaths renders a capped, comma-separated path list.
func joinPaths(paths []string) string {
	slices.Sort(paths)
	if len(paths) <= maxListedPaths {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(paths[:maxListedPaths], ", "), len(paths)-maxListedPaths)
}

// rejectUnknowns builds the strict-mode rejection. Advice is grouped by kind
// rather than repeated per field: a document piped from `get -o json` trips
// a dozen response-only fields at once, and one sentence per field made the
// message unreadable. The escape hatch is only offered when at least one
// field might genuinely be new: a field with a did-you-mean hint is a typo
// (sending it would send the misspelling), and the other kinds would send a
// write the API discards.
//
// Two wording rules keep it honest and readable. Schema facts are stated as
// facts about this build ("not in this CLI's update schema"), not about the
// API — the model is a pinned snapshot and the API may have moved. And when
// the document looks like a `get` response, that diagnosis is the whole
// message, because it is the one sentence the reader needs.
func rejectUnknowns(unknowns []input.Unknown) *clierr.Error {
	if len(unknowns) == 0 {
		return nil
	}
	var novel, createOnly, responseOnly []string
	var relocated, misplaced, explained []string
	for _, u := range unknowns {
		if u.Hint != "" {
			continue // a typo: the description already carries the did-you-mean, and the hatch must not be offered
		}
		kind, at := classifyUnknown(u.Path)
		switch kind {
		case unknownCreateOnly:
			if reason := createOnlyReason(u.Path); reason != "" {
				explained = append(
					explained,
					fmt.Sprintf(`"%s" is not in this CLI's update schema; %s`, u.Path, reason),
				)
				continue
			}
			createOnly = append(createOnly, u.Path)
		case unknownResponseOnly:
			responseOnly = append(responseOnly, u.Path)
		case unknownRelocated:
			relocated = append(relocated, fmt.Sprintf(`"%s" belongs at "%s" in requests`, u.Path, at))
		case unknownMisplaced:
			misplaced = append(misplaced, fmt.Sprintf(`"%s" belongs at "%s"`, u.Path, at))
		default:
			novel = append(novel, u.Path)
		}
	}

	single := len(unknowns) == 1
	var clauses []string
	if len(createOnly) > 0 {
		if len(createOnly) == 1 {
			clauses = append(clauses, fmt.Sprintf(
				`"%s" is not in this CLI's update schema (create accepts it); recreate the group to change it`,
				createOnly[0],
			))
		} else {
			clauses = append(clauses, fmt.Sprintf(
				"not in this CLI's update schema, though create accepts them (%s); recreate the group to change them",
				joinPaths(createOnly),
			))
		}
	}
	if len(responseOnly) > 0 {
		if len(responseOnly) == 1 {
			clauses = append(
				clauses,
				fmt.Sprintf(`"%s" is response-only in this CLI's API model and cannot be sent`, responseOnly[0]),
			)
		} else {
			clauses = append(clauses, fmt.Sprintf(
				"response-only in this CLI's API model and cannot be sent (%s)", joinPaths(responseOnly),
			))
		}
	}
	clauses = append(clauses, explained...)
	clauses = append(clauses, relocated...)
	clauses = append(clauses, misplaced...)
	if len(novel) > 0 {
		if single {
			// The description already names the one field; "unrecognized
			// (x)" would say it a second time.
			clauses = append(clauses, "re-run with --allow-unknown-fields to send it anyway")
		} else {
			clauses = append(clauses, fmt.Sprintf(
				"unrecognized (%s); re-run with --allow-unknown-fields to send them anyway", joinPaths(novel),
			))
		}
	}

	full := input.UnknownFieldsError("", unknowns, "")
	if len(clauses) > 0 {
		full.WithHint("%s", strings.Join(clauses, "; "))
	}
	// The commonest mistake, editing a `get` document and sending the whole
	// thing back as a patch, gets only its diagnosis and the command to start
	// from; the per-field list goes to --debug.
	if len(responseOnly) >= 3 {
		if api.Debugging() {
			fprintf(os.Stderr, "debug: %s; %s\n", full.Message, full.HintText())
		}
		return clierr.New(clierr.CodeInvalidInput, clierr.KindUnknownField,
			"this document is a get response, not a patch (%d of its fields only responses carry)", len(responseOnly)).
			WithHint("to get a document update -f accepts, run saladcloud container-groups get <group> --writable; --debug lists the fields")
	}
	return full
}

// namingGroup fills the group into a rejection's get <group> --writable
// when the command knows it: update does, so the command to type is exact.
func namingGroup(cerr *clierr.Error, group string) *clierr.Error {
	if cerr == nil || group == "" || cerr.Hint == nil {
		return cerr
	}
	hint := strings.Replace(*cerr.Hint, "get <group> --writable", "get "+shellWord(group)+" --writable", 1)
	cerr.Hint = &hint
	return cerr
}

// adviseUnknownShort is the one-field explanation used when a field is
// forced through with --allow-unknown-fields; "" for a novel field, which
// needs no extra caveat beyond "sent anyway".
func adviseUnknownShort(path string) string {
	switch kind, at := classifyUnknown(path); kind {
	case unknownCreateOnly:
		if reason := createOnlyReason(path); reason != "" {
			return reason
		}
		return "it is not in this CLI's update schema (create accepts it)"
	case unknownResponseOnly:
		return "it is response-only in this CLI's API model"
	case unknownRelocated:
		return fmt.Sprintf(`requests take it at "%s"`, at)
	case unknownMisplaced:
		return fmt.Sprintf(`it belongs at "%s"`, at)
	}
	return ""
}

// warnUnknowns reports each tolerated unknown field on w (stderr). Two cases
// get a stronger warning than "sent anyway":
//
//   - createMode array fields are dropped, not passed through: create
//     rebuilds arrays from its typed request.
//   - a create-only, response-only or relocated field will be accepted by
//     the API and silently ignored, so "sent to the API anyway" on its own
//     would let the caller believe it took effect.
func warnUnknowns(w io.Writer, unknowns []input.Unknown, createMode bool) {
	for _, u := range unknowns {
		if createMode && u.InArray {
			fprintf(
				w,
				"warning: %s; dropped, because create rebuilds array fields from its API model; set it with update -f instead\n",
				u.Desc,
			)
			continue
		}
		if advice := adviseUnknownShort(u.Path); advice != "" {
			fprintf(
				w,
				"warning: %s; sent to the API anyway (--allow-unknown-fields), but expect it to be ignored: %s\n",
				u.Desc,
				advice,
			)
			continue
		}
		fprintf(w, "warning: %s; sent to the API anyway (--allow-unknown-fields)\n", u.Desc)
	}
}
