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
func requestToResponsePath() map[string]string {
	return map[string]string{
		"container.priority": "priority",
	}
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
	trees, ok := intentDiffTrees(patch, before, after)
	if !ok {
		return nil
	}

	var leaves []patchLeaf
	collectLeaves(trees.patch, "", &leaves)

	var out []ignoredField
	for _, leaf := range leaves {
		if f, ignored := ignoredLeaf(leaf, observeLeaf(&trees, leaf.Path)); ignored {
			out = append(out, f)
		}
	}
	slices.SortStableFunc(out, func(a, b ignoredField) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// intentTrees are the generic trees the update-side intent diffs compare:
// the patch, and the group before and after the write.
type intentTrees struct {
	patch, before, after any
}

// intentDiffTrees decodes the patch and the two group states for
// findIgnoredFields and findUnrequestedChanges. It reports false — nothing
// to compare — when either state is missing, the change is still pending
// (the after-state isn't authoritative yet), or a document does not decode.
func intentDiffTrees(patch []byte, before, after *shared.ContainerGroup) (intentTrees, bool) {
	var trees intentTrees
	if before == nil || after == nil {
		return trees, false
	}
	if ptr.Deref(after.PendingChange) {
		return trees, false // still settling; the after-state isn't authoritative yet
	}
	if err := json.Unmarshal(patch, &trees.patch); err != nil {
		return trees, false
	}
	var err error
	if trees.before, err = toTree(before); err != nil {
		return trees, false
	}
	if trees.after, err = toTree(after); err != nil {
		return trees, false
	}
	return trees, true
}

// observedLeaf is one patch path's value in the group before and after the
// write; the OK fields report whether the path exists there at all.
type observedLeaf struct {
	before, after     any
	beforeOK, afterOK bool
}

// observeLeaf looks path up in the before and after trees, falling back to
// the response's own spelling for known asymmetries when neither has it.
func observeLeaf(trees *intentTrees, path string) observedLeaf {
	var o observedLeaf
	o.before, o.beforeOK = lookupPath(trees.before, path)
	o.after, o.afterOK = lookupPath(trees.after, path)
	// Fall back to the response's own spelling for known asymmetries.
	if !o.beforeOK && !o.afterOK {
		if alias, ok := requestToResponsePath()[path]; ok {
			o.before, o.beforeOK = lookupPath(trees.before, alias)
			o.after, o.afterOK = lookupPath(trees.after, alias)
		}
	}
	return o
}

// ignoredLeaf is findIgnoredFields' verdict on one patch leaf, given what
// the before and after states hold at its path: the report, and whether the
// write was ignored at all.
func ignoredLeaf(leaf patchLeaf, o observedLeaf) (ignoredField, bool) {
	if !o.beforeOK && !o.afterOK {
		return ignoredField{}, false // unobservable in the response; don't guess
	}
	if jsonEqual(o.after, leaf.Value) {
		return ignoredField{}, false // applied
	}
	moved := !jsonEqual(o.after, o.before)
	if moved && !o.afterOK {
		// The field changed and the response no longer exposes it —
		// typically a clear ([] / null) that omitempty drops from the
		// document. Its current value is unobservable, so say nothing
		// rather than report "got null" for a write that applied.
		return ignoredField{}, false
	}
	if moved && leaf.Value == nil && o.after != nil {
		return ignoredField{}, false // null request answered with a server default: normalization, not a drop
	}
	return ignoredField{
		Path:      leaf.Path,
		Requested: shownValue(leaf.Path, leaf.Value),
		Actual:    shownValue(leaf.Path, o.after),
		Moved:     moved,
	}, true
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
		return nil, fmt.Errorf("encoding the group: %w", err)
	}
	var t any
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("decoding the group: %w", err)
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
	tail := pathKeys(path)
	return api.SecretField(tail.key, tail.parent)
}

// pathTail is the last two segments of a dotted path: its key, and the key
// of the object holding it ("" at the top level).
type pathTail struct {
	key, parent string
}

// pathKeys returns the last two segments of a dotted path.
func pathKeys(path string) pathTail {
	segs := strings.Split(path, ".")
	tail := pathTail{key: segs[len(segs)-1], parent: ""}
	if len(segs) > 1 {
		tail.parent = segs[len(segs)-2]
	}
	return tail
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
	tail := pathKeys(path)
	return compactValue(redactNested(v, tail.key, tail.parent))
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

// fieldsNoun is "field" for n of one or fewer, else "fields".
func fieldsNoun(n int) string {
	if n > 1 {
		return "fields"
	}
	return "field"
}

// warnIgnoredFields reports dropped writes on stderr. Warnings always go to
// stderr, including in json mode, so the stdout one-document contract holds.
func warnIgnoredFields(w io.Writer, fields []ignoredField) {
	if len(fields) == 0 {
		return
	}
	fprintf(w, "warning: the API accepted this update but did not apply %d %s:\n", len(fields), fieldsNoun(len(fields)))
	for _, f := range fields {
		if f.Moved {
			fprintf(w, "warning:   %s: requested %s, got %s\n", f.Path, f.Requested, f.Actual)
			continue
		}
		fprintf(w, "warning:   %s: requested %s, still %s\n", f.Path, f.Requested, f.Actual)
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
func remediation() map[string]string {
	return map[string]string{
		"scheduled-scaling-enabled": "scheduled scaling is on hold (the flag is false; the actions are kept); " +
			"to re-enable it, put " + reenableSchedule,
		"container.environment_variables": "the API replaces the whole environment_variables map rather than " +
			"merging it, so keys you did not mention are dropped; send the complete desired map",
	}
}

// reenableSchedule is the fix for scheduled scaling left on hold, with no
// shell syntax: a patch file works in every shell.
const reenableSchedule = `{"scheduled-scaling-enabled": true} in a file and run ` +
	`saladcloud container-groups update <group> -f <file>`

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
	trees, ok := intentDiffTrees(patch, before, after)
	if !ok {
		return nil
	}
	var leaves []patchLeaf
	collectLeaves(trees.patch, "", &leaves)
	named := namedPatchPaths(leaves)

	var out []unrequestedChange
	collectChanges(trees.before, trees.after, "", named, &out)
	slices.SortStableFunc(out, func(a, b unrequestedChange) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// namedPatchPaths is every path the patch requested: a path counts as
// requested if the patch named it OR anything under it. Setting a whole
// object (liveness_probe: {...}) names only its leaves, but when the field
// was previously absent the trees diverge at the object itself — without the
// ancestors marked, deliberately adding a probe would be reported as an
// unrequested side effect.
func namedPatchPaths(leaves []patchLeaf) map[string]bool {
	named := make(map[string]bool)
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
	return named
}

// collectChanges walks the before/after trees together, descending while both
// sides are objects and comparing at every other node.
func collectChanges(beforeVal, afterVal any, path string, named map[string]bool, out *[]unrequestedChange) {
	bObj, bOK := beforeVal.(map[string]any)
	aObj, aOK := afterVal.(map[string]any)
	if bOK && aOK {
		for _, k := range sortedKeyUnion(bObj, aObj) {
			child := k
			if path != "" {
				child = path + "." + k
			}
			collectChanges(bObj[k], aObj[k], child, named, out)
		}
		return
	}
	if c, changed := unrequestedLeafChange(path, named, beforeVal, afterVal); changed {
		*out = append(*out, c)
	}
}

// sortedKeyUnion is the keys of a and b together, sorted, each once.
func sortedKeyUnion(a, b map[string]any) []string {
	keys := make(map[string]bool)
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

// unrequestedLeafChange is collectChanges' verdict on one node that is not
// an object on both sides: the change, and whether it is one to report (a
// caller-writable field the patch did not name, whose value moved).
func unrequestedLeafChange(path string, named map[string]bool, beforeVal, afterVal any) (unrequestedChange, bool) {
	if path == "" || named[path] || jsonEqual(beforeVal, afterVal) {
		return unrequestedChange{}, false
	}
	if !callerWritable(path) {
		return unrequestedChange{}, false // server-managed churn (update_time, hash, current_state, …)
	}
	if secretPath(path) {
		return unrequestedChange{
			Path:   path,
			Before: secretChange(beforeVal, afterVal),
			Secret: true,
			After:  "",
			Note:   false,
		}, true
	}
	return unrequestedChange{
		Path:   path,
		Before: shownValue(path, beforeVal),
		After:  shownValue(path, afterVal),
		Note:   holdRule(path, named, beforeVal, afterVal),
		Secret: false,
	}, true
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
		warnUnrequestedList(w, warns)
	}
	for _, c := range notes {
		// Only one rule is classified today (holdRule), so its sentence is
		// spelled out here rather than routed through the remediation map,
		// whose entry is worded for the flag flipping WITHOUT a replicas write.
		fprintf(
			w,
			"note: %s: %s -> %s; setting replicas puts scheduled scaling on hold (actions kept); "+
				"to re-enable it, put %s\n",
			c.Path,
			c.Before,
			c.After,
			reenableSchedule,
		)
	}
}

// warnUnrequestedList is warnUnrequestedChanges' warning block: the count,
// up to maxReportedChanges of the changes, and the remediation for each
// cause among those shown.
func warnUnrequestedList(w io.Writer, warns []unrequestedChange) {
	shown := warns
	extra := 0
	if len(shown) > maxReportedChanges {
		extra = len(shown) - maxReportedChanges
		shown = shown[:maxReportedChanges]
	}
	fprintf(
		w,
		"warning: this update also changed %d %s the patch did not mention:\n",
		len(warns),
		fieldsNoun(len(warns)),
	)
	for _, c := range shown {
		if c.Secret {
			fprintf(w, "warning:   %s: %s\n", c.Path, c.Before)
			continue
		}
		fprintf(w, "warning:   %s: %s -> %s\n", c.Path, c.Before, c.After)
	}
	if extra > 0 {
		fprintf(w, "warning:   and %d more\n", extra)
	}
	warnRemediations(w, shown)
}

// warnRemediations prints the remediation for each cause among changes, once
// per cause rather than once per affected key.
func warnRemediations(w io.Writer, changes []unrequestedChange) {
	seen := make(map[string]bool)
	for _, c := range changes {
		if at, ok := remediationFor(c.Path); ok && !seen[at] {
			seen[at] = true
			fprintf(w, "warning:   %s\n", remediation()[at])
		}
	}
}

// remediationFor finds the most specific remediation covering a path,
// returning the remediation key it matched (so the advice is printed once
// per cause rather than once per affected key), and false when none does.
func remediationFor(path string) (string, bool) {
	for p := path; p != ""; {
		if _, ok := remediation()[p]; ok {
			return p, true
		}
		i := strings.LastIndexByte(p, '.')
		if i < 0 {
			break
		}
		p = p[:i]
	}
	return "", false
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
	trees, decoded := createDiffTrees(request, created)
	if !decoded {
		return nil
	}
	var leaves []patchLeaf
	collectLeaves(trees.request, "", &leaves)

	var out []ignoredField
	dropped := make(map[string]bool)
	for _, leaf := range leaves {
		got, ok := lookupCreatedLeaf(trees.created, leaf.Path)
		if !ok {
			// Anything the response leaves out is unobservable, except an
			// object dropped whole, which is reported once.
			if f, whole := droppedCreateObject(leaf, &trees, dropped); whole {
				out = append(out, f)
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
			Moved:     false,
		})
	}
	slices.SortStableFunc(out, func(a, b ignoredField) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// createTrees are the generic trees findUnappliedCreateFields compares: the
// create request and the created group.
type createTrees struct {
	request, created any
}

// createDiffTrees decodes the create request and the created group for
// findUnappliedCreateFields; false when either does not decode.
func createDiffTrees(request []byte, created *shared.ContainerGroup) (createTrees, bool) {
	var trees createTrees
	if err := json.Unmarshal(request, &trees.request); err != nil {
		return trees, false
	}
	// The response encoded like the request (MarshalRequest), so the empty
	// lists it holds compare equal to the empty lists sent.
	b, err := output.MarshalRequest(created)
	if err != nil {
		return trees, false
	}
	if err := json.Unmarshal(b, &trees.created); err != nil {
		return trees, false
	}
	return trees, true
}

// lookupCreatedLeaf looks a request path up in the created group, under the
// response's own spelling for known asymmetries when the path is absent.
func lookupCreatedLeaf(createdTree any, path string) (any, bool) {
	got, ok := lookupPath(createdTree, path)
	if !ok {
		if alias, aliased := requestToResponsePath()[path]; aliased {
			got, ok = lookupPath(createdTree, alias)
		}
	}
	return got, ok
}

// droppedCreateObject reports a request leaf the created group does not
// show whose top-level object the response could carry and does not: the
// object was dropped whole (queue_autoscaler on create). dropped records
// the objects already reported, so each is reported once.
func droppedCreateObject(leaf patchLeaf, trees *createTrees, dropped map[string]bool) (ignoredField, bool) {
	top, _, _ := strings.Cut(leaf.Path, ".")
	if leaf.Value == nil || dropped[top] || !responseHasField(top) {
		return ignoredField{}, false
	}
	if v, present := lookupPath(trees.created, top); present && v != nil {
		return ignoredField{}, false
	}
	dropped[top] = true
	req, _ := lookupPath(trees.request, top)
	return ignoredField{Path: top, Requested: shownValue(top, req), Actual: "null", Moved: false}, true
}

// responseHasField reports whether a container group response can carry
// the top-level key, by the SDK's json tags, so its absence means the API
// dropped it.
func responseHasField(key string) bool {
	t := reflect.TypeFor[shared.ContainerGroup]()
	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" && name == key {
			return true
		}
	}
	return false
}

// warnUnappliedCreateFields is warnIgnoredFields with create's wording.
func warnUnappliedCreateFields(w io.Writer, fields []ignoredField) {
	if len(fields) == 0 {
		return
	}
	fprintf(w, "warning: the group was created but the API did not apply %d requested %s:\n",
		len(fields), fieldsNoun(len(fields)))
	for _, f := range fields {
		fprintf(w, "warning:   %s: requested %s, got %s\n", f.Path, f.Requested, f.Actual)
	}
}
