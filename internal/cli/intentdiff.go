package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/input"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// ignoredField is one field the caller's patch named that the API accepted
// (HTTP 200) and then did not apply.
type ignoredField struct {
	Path      string // dotted location in the patch document
	Requested string // compact JSON of the value the caller asked for
	Actual    string // compact JSON of the value the field holds now
	// Moved is true when the field changed, just not to the requested
	// value (requested true, before true, after false). False means it
	// stayed put.
	Moved bool
}

// requestToResponsePath maps patch-document paths onto the different path the
// same field occupies in a group response. Only genuine shape asymmetries in
// the API belong here — not behavior. Verified: create/patch take
// container.priority while every response returns priority at the top level.
//
// Without this the diff would compare an absent path against an absent path
// and report a *successful* GPU-group priority change as dropped.
var requestToResponsePath = map[string]string{
	"container.priority": "priority",
}

// findIgnoredFields compares a merge patch against the group state before and
// after the write, and reports every field the patch named whose value did
// not move.
//
// Why this exists: the API accepts writes to some fields and silently drops
// them (container.image_caching always; container.priority on
// CPU-only groups; every networking field except port when forced through
// with --allow-unknown-fields). Each returns 200, so a caller has no way to
// know its instruction was discarded. This turns that into a visible warning.
//
// It is deliberately a runtime diff rather than a table of known-bad fields.
// A table encodes our beliefs about someone else's service and rots: an
// earlier revision of this repo asserted "container.priority is create-time
// only" in README on the strength of a CPU-only test, which is false for GPU
// groups. Comparing the caller's own instruction against observed state
// cannot be wrong that way, and it catches fields that start misbehaving
// later with no code change.
//
// Three guards keep false positives near zero:
//
//   - A field is reported when its final value is not what was requested,
//     whether it stayed put or moved somewhere else (requesting
//     scheduled-scaling-enabled true alongside replicas ends with the flag
//     false: that moved, and it is exactly the kind of thing to report).
//     Asking for a value a field already holds is a legitimate no-op, not
//     a dropped write. One normalization is allowed for: a request to null
//     that the server answers with a fresh non-null value (patching
//     display_name to null yields the group name) is a server default
//     being filled in, not a discarded write, and stays quiet.
//   - Nothing is reported while pending_change is true. Container changes
//     settle asynchronously and the response lags a version, so a pending
//     write would otherwise look ignored. update settles by default before
//     this runs, so the guard only bites under --no-wait, where the caller
//     has opted out of verification.
//   - A field absent from both the before and after documents is skipped
//     rather than guessed at: the response simply does not expose it, so
//     whether it applied is unobservable from here.
//
// Note that `version` is not usable as a did-it-apply signal — a replicas
// patch was observed changing state without incrementing it — which is why
// this compares field values.
func findIgnoredFields(patch []byte, before, after *shared.ContainerGroup) []ignoredField {
	if before == nil || after == nil {
		return nil
	}
	if ptr.Deref(after.PendingChange) {
		return nil // still settling; the after-state isn't authoritative yet
	}
	var patchTree any
	if err := json.Unmarshal(patch, &patchTree); err != nil {
		return nil
	}
	beforeTree, err := toTree(before)
	if err != nil {
		return nil
	}
	afterTree, err := toTree(after)
	if err != nil {
		return nil
	}

	var leaves []patchLeaf
	collectLeaves(patchTree, "", &leaves)

	var out []ignoredField
	for _, leaf := range leaves {
		path := leaf.Path
		beforeVal, beforeOK := lookupPath(beforeTree, path)
		afterVal, afterOK := lookupPath(afterTree, path)
		// Fall back to the response's own spelling for known asymmetries.
		if !beforeOK && !afterOK {
			if alias, ok := requestToResponsePath[path]; ok {
				beforeVal, beforeOK = lookupPath(beforeTree, alias)
				afterVal, afterOK = lookupPath(afterTree, alias)
			}
		}
		if !beforeOK && !afterOK {
			continue // unobservable in the response; don't guess
		}
		if jsonEqual(afterVal, leaf.Value) {
			continue // applied
		}
		moved := !jsonEqual(afterVal, beforeVal)
		if moved && !afterOK {
			// The field changed and the response no longer exposes it —
			// typically a clear ([] / null) that omitempty drops from the
			// document. Its current value is unobservable, so say nothing
			// rather than report "got null" for a write that applied.
			continue
		}
		if moved && leaf.Value == nil && afterVal != nil {
			continue // null request answered with a server default: normalization, not a drop
		}
		out = append(out, ignoredField{
			Path:      path,
			Requested: shownValue(path, leaf.Value),
			Actual:    shownValue(path, afterVal),
			Moved:     moved,
		})
	}
	slices.SortStableFunc(out, func(a, b ignoredField) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// patchLeaf is one asserted value in a patch document, with its dotted path.
type patchLeaf struct {
	Path  string
	Value any
}

// collectLeaves descends the patch document. Objects recurse (merge-patch
// merges them); every other value — scalar, array, or null — is a leaf that
// asserts a final value.
func collectLeaves(v any, path string, out *[]patchLeaf) {
	if obj, ok := v.(map[string]any); ok {
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			child := k
			if path != "" {
				child = path + "." + k
			}
			collectLeaves(obj[k], child, out)
		}
		return
	}
	*out = append(*out, patchLeaf{Path: path, Value: v})
}

// lookupPath resolves a dotted path in a generic tree. The bool reports
// whether the path exists at all, which is what separates "the response says
// this is null" from "the response has no such field".
func lookupPath(tree any, path string) (any, bool) {
	cur := tree
	for seg := range strings.SplitSeq(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, present := obj[seg]
		if !present {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// toTree round-trips a group through JSON into a generic tree, so patch
// paths can be looked up by their wire names (including the kebab-case
// scheduled-scaling fields) without mirroring the struct layout.
func toTree(g *shared.ContainerGroup) (any, error) {
	b, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}
	var t any
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return t, nil
}

// jsonEqual compares two generic values by their canonical JSON encoding.
func jsonEqual(a, b any) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return bytes.Equal(ja, jb)
}

// compactValue renders a value for the warning text.
func compactValue(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	return string(b)
}

// secretPath reports whether the value at a dotted path is a credential, by
// the rule --debug=body redacts with (api.SecretField): an environment
// variable's value, a header's value, or a credential field.
func secretPath(path string) bool {
	if strings.HasPrefix(path, "container.environment_variables.") {
		return true // a variable's name may itself contain dots
	}
	key, parent := pathKeys(path)
	return api.SecretField(key, parent)
}

// pathKeys returns the last two segments of a dotted path.
func pathKeys(path string) (key, parent string) {
	segs := strings.Split(path, ".")
	key = segs[len(segs)-1]
	if len(segs) > 1 {
		parent = segs[len(segs)-2]
	}
	return key, parent
}

// redactNested returns v with every credential inside it replaced, walking
// objects and arrays the way --debug=body does: an array's elements are
// read under the array's own key.
func redactNested(v any, key, parent string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, c := range t {
			out[k] = redactNested(c, k, key)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, c := range t {
			out[i] = redactNested(c, key, parent)
		}
		return out
	case string:
		if api.SecretField(key, parent) {
			return "REDACTED"
		}
	}
	return v
}

// shownValue renders the value at path for a warning: "(hidden)" for a
// credential, otherwise the value with any credentials inside it redacted.
func shownValue(path string, v any) string {
	if v != nil && secretPath(path) {
		return "(hidden)"
	}
	key, parent := pathKeys(path)
	return compactValue(redactNested(v, key, parent))
}

// secretChange describes a credential's change without its value.
func secretChange(before, after any) string {
	switch {
	case before == nil:
		return "(set)"
	case after == nil:
		return "(removed)"
	}
	return "(changed)"
}

// warnIgnoredFields reports dropped writes on stderr. Warnings always go to
// stderr, including in json mode, so the stdout one-document contract holds.
func warnIgnoredFields(w io.Writer, fields []ignoredField) {
	if len(fields) == 0 {
		return
	}
	noun := "field"
	if len(fields) > 1 {
		noun = "fields"
	}
	fmt.Fprintf(w, "warning: the API accepted this update but did not apply %d %s:\n", len(fields), noun)
	for _, f := range fields {
		if f.Moved {
			fmt.Fprintf(w, "warning:   %s: requested %s, got %s\n", f.Path, f.Requested, f.Actual)
			continue
		}
		fmt.Fprintf(w, "warning:   %s: requested %s, still %s\n", f.Path, f.Requested, f.Actual)
	}
}

// ---------------------------------------------------------------------------
// The inverse direction: fields that moved without the patch asking.
//
// findIgnoredFields above answers "did what I asked for happen?". This answers
// "did anything ELSE happen?", which is where the API's two silent-data-loss
// behaviors live: an unrelated patch clears scheduled-scaling-enabled, and
// environment_variables is replace-not-merge so unmentioned keys vanish.
//
// The hard part is noise: a group document churns on its own (update_time,
// version, current_state, instance counts, container.hash/size, the assigned
// DNS name). Rather than hand-maintain an exclusion list, this derives it —
// a field is server-managed exactly when it appears in neither the create
// request nor the patch schema, i.e. when no caller could have set it. That
// keeps working as the schemas evolve, which a literal list would not.
//
// It stays silent on ordinary patches and catches the scheduled-scaling
// reset. One report is truthful but expected, and is worth naming because
// it is a deliberate tradeoff rather than an oversight.
// Switching a probe's handler — patching startup_probe to a grpc handler
// when it held an http one — reports `startup_probe.http: {...} -> null`.
// That is a correct observation (merge-patch would have kept the old
// handler; the API replaced the object) but not news to whoever asked for
// the switch. It cannot be suppressed structurally: "a sibling key vanished
// under a parent the patch named" is the *identical* shape to
// environment_variables key loss, which is the most valuable thing this
// check finds. Losing that to silence a rare probe-switch message would be
// the wrong trade, so both are reported.
// ---------------------------------------------------------------------------

// unrequestedChange is one field that changed although the patch never
// named it.
type unrequestedChange struct {
	Path   string
	Before string
	After  string
	// Secret marks a credential's change: Before holds "(set)",
	// "(changed)" or "(removed)" and no value is ever shown.
	Secret bool
	// Note marks a change that is a documented platform rule working as
	// designed rather than a surprise: reported as "note:" instead of
	// "warning:", because a warning on expected behavior teaches people to
	// ignore warnings.
	Note bool
}

// maxReportedChanges caps the inverse report; a pathological diff should not
// produce a wall of stderr.
const maxReportedChanges = 8

// remediation carries extra advice for paths where the cause is known and
// there is a concrete fix. This is not a detection list — detection stays
// fully dynamic — so if the API stops misbehaving the entry simply never
// fires again.
var remediation = map[string]string{
	"scheduled-scaling-enabled":       "scheduled scaling is on hold (the flag is false; the actions are kept); to re-enable it, put " + reenableSchedule,
	"container.environment_variables": "the API replaces the whole environment_variables map rather than merging it, so keys you did not mention are dropped; send the complete desired map",
}

// reenableSchedule is the fix for scheduled scaling left on hold, with no
// shell syntax: a patch file works in every shell.
const reenableSchedule = `{"scheduled-scaling-enabled": true} in a file and run saladcloud container-groups update <group> -f <file>`

// holdRule is the one documented platform rule the inverse diff knows the
// meaning of: writing `replicas` puts scheduled scaling on hold — the flag
// goes false and the actions are kept — the way a manual thermostat
// setpoint overrides the program. Only replicas (and therefore `scale`)
// trips it, and an explicit
// scheduled-scaling-enabled true in the same patch does not survive. It is
// deliberate, so the report is a note, not a warning. Detection stays
// dynamic; this only classifies what the diff found.
func holdRule(path string, named map[string]bool, before, after any) bool {
	return path == "scheduled-scaling-enabled" && named["replicas"] &&
		jsonEqual(before, true) && jsonEqual(after, false)
}

// callerWritable reports whether any request schema exposes this path, i.e.
// whether a caller could have caused the change. Everything else is
// server-managed churn and is excluded from the inverse report.
func callerWritable(path string) bool {
	return input.FieldPathKnown(reflect.TypeFor[containergroups.ContainerGroupCreationRequest](), path) ||
		input.FieldPathKnown(reflect.TypeFor[containergroups.ContainerGroupPatch](), path)
}

// findUnrequestedChanges reports caller-writable fields whose value moved
// even though the patch did not name them. Guards match findIgnoredFields:
// both states required, and nothing reported while the change is pending.
func findUnrequestedChanges(patch []byte, before, after *shared.ContainerGroup) []unrequestedChange {
	if before == nil || after == nil {
		return nil
	}
	if ptr.Deref(after.PendingChange) {
		return nil
	}
	var patchTree any
	if err := json.Unmarshal(patch, &patchTree); err != nil {
		return nil
	}
	beforeTree, err := toTree(before)
	if err != nil {
		return nil
	}
	afterTree, err := toTree(after)
	if err != nil {
		return nil
	}

	// A path counts as requested if the patch named it OR anything under it.
	// Setting a whole object (liveness_probe: {...}) names only its leaves,
	// but when the field was previously absent the trees diverge at the
	// object itself — without the ancestors marked, deliberately adding a
	// probe would be reported as an unrequested side effect.
	named := make(map[string]bool)
	var leaves []patchLeaf
	collectLeaves(patchTree, "", &leaves)
	for _, l := range leaves {
		for p := l.Path; p != ""; {
			named[p] = true
			i := strings.LastIndexByte(p, '.')
			if i < 0 {
				break
			}
			p = p[:i]
		}
	}

	var out []unrequestedChange
	collectChanges(beforeTree, afterTree, "", named, &out)
	slices.SortStableFunc(out, func(a, b unrequestedChange) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// collectChanges walks the before/after trees together, descending while both
// sides are objects and comparing at every other node.
func collectChanges(beforeVal, afterVal any, path string, named map[string]bool, out *[]unrequestedChange) {
	bObj, bOK := beforeVal.(map[string]any)
	aObj, aOK := afterVal.(map[string]any)
	if bOK && aOK {
		keys := make(map[string]bool)
		for k := range bObj {
			keys[k] = true
		}
		for k := range aObj {
			keys[k] = true
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		slices.Sort(names)
		for _, k := range names {
			child := k
			if path != "" {
				child = path + "." + k
			}
			collectChanges(bObj[k], aObj[k], child, named, out)
		}
		return
	}
	if path == "" || named[path] || jsonEqual(beforeVal, afterVal) {
		return
	}
	if !callerWritable(path) {
		return // server-managed churn (update_time, hash, current_state, …)
	}
	if secretPath(path) {
		*out = append(*out, unrequestedChange{Path: path, Before: secretChange(beforeVal, afterVal), Secret: true})
		return
	}
	*out = append(*out, unrequestedChange{
		Path:   path,
		Before: shownValue(path, beforeVal),
		After:  shownValue(path, afterVal),
		Note:   holdRule(path, named, beforeVal, afterVal),
	})
}

// warnUnrequestedChanges reports side effects on stderr: the affected keys
// first, then one remediation hint per cause (deepest-matching remediation
// wins, so a change under container.environment_variables gets the
// map-semantics advice). Changes classified as a documented rule (Note) are
// printed after the warnings, on their own "note:" lines, so a hold on
// scheduled scaling reads as information rather than alarm.
func warnUnrequestedChanges(w io.Writer, changes []unrequestedChange) {
	var warns, notes []unrequestedChange
	for _, c := range changes {
		if c.Note {
			notes = append(notes, c)
		} else {
			warns = append(warns, c)
		}
	}
	if len(warns) > 0 {
		shown := warns
		extra := 0
		if len(shown) > maxReportedChanges {
			extra = len(shown) - maxReportedChanges
			shown = shown[:maxReportedChanges]
		}
		noun := "field"
		if len(warns) > 1 {
			noun = "fields"
		}
		fmt.Fprintf(w, "warning: this update also changed %d %s the patch did not mention:\n", len(warns), noun)
		for _, c := range shown {
			if c.Secret {
				fmt.Fprintf(w, "warning:   %s: %s\n", c.Path, c.Before)
				continue
			}
			fmt.Fprintf(w, "warning:   %s: %s -> %s\n", c.Path, c.Before, c.After)
		}
		if extra > 0 {
			fmt.Fprintf(w, "warning:   and %d more\n", extra)
		}
		seen := make(map[string]bool)
		for _, c := range shown {
			if hint, at := remediationFor(c.Path); hint != "" && !seen[at] {
				seen[at] = true
				fmt.Fprintf(w, "warning:   %s\n", hint)
			}
		}
	}
	for _, c := range notes {
		// Only one rule is classified today (holdRule), so its sentence is
		// spelled out here rather than routed through the remediation map,
		// whose entry is worded for the flag flipping WITHOUT a replicas write.
		fmt.Fprintf(
			w,
			"note: %s: %s -> %s; setting replicas puts scheduled scaling on hold (actions kept); to re-enable it, put %s\n",
			c.Path,
			c.Before,
			c.After,
			reenableSchedule,
		)
	}
}

// remediationFor finds the most specific remediation covering a path,
// returning the advice and the key it matched (so it is printed once per
// cause rather than once per affected key).
func remediationFor(path string) (string, string) {
	for p := path; p != ""; {
		if hint, ok := remediation[p]; ok {
			return hint, p
		}
		i := strings.LastIndexByte(p, '.')
		if i < 0 {
			break
		}
		p = p[:i]
	}
	return "", ""
}

// findUnappliedCreateFields is findIgnoredFields for create, where there is
// no before-state: a field the request named is reported when the created
// group came back holding something else.
//
// Two differences from the update path, both deliberate:
//
//   - No pending_change guard. A create response always reports
//     pending_change true (that describes the deployment, not config
//     staging), so applying the update guard here would silence the check
//     entirely. The created document is the created config.
//   - No "unchanged from before" guard, because there is no before. What
//     keeps normalization quiet instead is that only fields the request
//     actually named are compared — the defaults create fills (display_name
//     from name, command to [], shm_size to 64) are never named, so they
//     are never reported.
//
// The request must be the *effective* document (flags merged over the file),
// or every flag override would look like a dropped write.
//
// A field the response does not show is unobservable and not reported,
// except a whole top-level object the response schema has and the created
// group lacks: the API drops queue_autoscaler on create that way.
//
// The fields it finds in practice are container.image_caching and
// queue_autoscaler, both of which the API drops on create.
func findUnappliedCreateFields(request []byte, created *shared.ContainerGroup) []ignoredField {
	if created == nil {
		return nil
	}
	var reqTree any
	if err := json.Unmarshal(request, &reqTree); err != nil {
		return nil
	}
	// The response encoded like the request (MarshalRequest), so the empty
	// lists it holds compare equal to the empty lists sent.
	b, err := output.MarshalRequest(created)
	if err != nil {
		return nil
	}
	var createdTree any
	if err := json.Unmarshal(b, &createdTree); err != nil {
		return nil
	}
	var leaves []patchLeaf
	collectLeaves(reqTree, "", &leaves)

	var out []ignoredField
	dropped := make(map[string]bool)
	for _, leaf := range leaves {
		got, ok := lookupPath(createdTree, leaf.Path)
		if !ok {
			if alias, aliased := requestToResponsePath[leaf.Path]; aliased {
				got, ok = lookupPath(createdTree, alias)
			}
		}
		if !ok {
			// A top-level object the response could carry and does not was
			// dropped whole (queue_autoscaler on create); report it once.
			// Anything else the response leaves out is unobservable.
			top, _, _ := strings.Cut(leaf.Path, ".")
			if leaf.Value != nil && !dropped[top] && responseHasField(top) {
				if v, present := lookupPath(createdTree, top); !present || v == nil {
					dropped[top] = true
					req, _ := lookupPath(reqTree, top)
					out = append(out, ignoredField{Path: top, Requested: shownValue(top, req), Actual: "null"})
				}
			}
			continue
		}
		if jsonEqual(got, leaf.Value) {
			continue
		}
		out = append(out, ignoredField{
			Path:      leaf.Path,
			Requested: shownValue(leaf.Path, leaf.Value),
			Actual:    shownValue(leaf.Path, got),
		})
	}
	slices.SortStableFunc(out, func(a, b ignoredField) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// responseFields are the top-level keys a container group response can
// carry, by the SDK's json tags.
var responseFields = func() map[string]bool {
	fields := make(map[string]bool)
	t := reflect.TypeFor[shared.ContainerGroup]()
	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			fields[name] = true
		}
	}
	return fields
}()

// responseHasField reports whether a container group response can carry
// the top-level key, so its absence means the API dropped it.
func responseHasField(key string) bool { return responseFields[key] }

// warnUnappliedCreateFields is warnIgnoredFields with create's wording.
func warnUnappliedCreateFields(w io.Writer, fields []ignoredField) {
	if len(fields) == 0 {
		return
	}
	noun := "field"
	if len(fields) > 1 {
		noun = "fields"
	}
	fmt.Fprintf(w, "warning: the group was created but the API did not apply %d requested %s:\n", len(fields), noun)
	for _, f := range fields {
		fmt.Fprintf(w, "warning:   %s: requested %s, got %s\n", f.Path, f.Requested, f.Actual)
	}
}
