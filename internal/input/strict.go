// Strict unknown-field checking for definition/patch files.
//
// yaml.UnmarshalStrict alone cannot do this job: the SDK's request structs
// implement json.Unmarshaler (for Nullable fields), and a custom unmarshaler
// opts a type out of strict field checking entirely — including the top-level
// request structs, so every typo'd field was a silent no-op. This walker
// re-checks the raw document against the field names derived from the SDK
// struct tags (reflection, recursive, honoring kebab-case names like
// "scaling-actions").
//
// Two modes share the walk: CheckUnknownFields rejects the first unknown
// path (the default, catching typos), and UnknownFieldList returns them all
// — the --allow-unknown-fields escape hatch downgrades rejection to
// warnings so a definition can carry API fields newer than this CLI's
// pinned SDK model.
package input

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// Unknown describes one field in a document that the target struct type
// does not know.
type Unknown struct {
	// Desc is the human description: `unknown field "x" in "y" (did you
	// mean "z"?)`.
	Desc string
	// Hint is the known field name the key most plausibly meant ("" when
	// there is no confident guess). Callers use it to decide advice: a
	// field with a hint is a typo, and offering --allow-unknown-fields for
	// it would send the misspelling.
	Hint string
	// Path is the field's dotted location in the document
	// ("networking.auth"), so callers can classify it against other
	// schemas — see cli.adviseUnknown.
	Path string
	// InArray is true when the field sits inside an array element — those
	// cannot be preserved by create's struct round-trip even with
	// --allow-unknown-fields (update's raw passthrough preserves them).
	InArray bool
}

// Detail is Desc without its leading "unknown field ".
func (u Unknown) Detail() string { return strings.TrimPrefix(u.Desc, "unknown field ") }

// UnknownFieldList walks the JSON document data against the (recursive)
// field set of struct type t and returns every unknown object key, in
// deterministic order. Fields typed `any` accept arbitrary sub-documents
// (e.g. an inference job's input); map keys are free-form but map values
// are checked against the map's value type.
func UnknownFieldList(data []byte, t reflect.Type) []Unknown {
	var out []Unknown
	collectUnknown(data, t, "", &out)
	return out
}

// maxReportedUnknowns caps how many offenders one message lists, so a
// wildly wrong document does not produce a wall of text.
const maxReportedUnknowns = 5

// CheckUnknownFields is UnknownFieldList as a pass/fail check: it returns a
// clierr.KindUnknownField error describing every unknown path (capped), or nil when
// the document is clean.
//
// Reporting all of them matters for a realistic workflow: piping `get -o
// json` into an edit and back through `update -f` trips several read-only
// fields at once, and naming one per run turns cleaning the document into N
// round-trips.
func CheckUnknownFields(data []byte, t reflect.Type) *clierr.Error {
	unknowns := UnknownFieldList(data, t)
	if len(unknowns) == 0 {
		return nil
	}
	return UnknownFieldsError("", unknowns, "")
}

// UnknownFieldsError is the unknown_field error for a non-empty list of
// unknown fields: the single-field wording when there is only one, else a
// count and a semicolon-separated list. lead and tail are added verbatim
// before and after (each "" or ending/starting with its own separator).
func UnknownFieldsError(lead string, unknowns []Unknown, tail string) *clierr.Error {
	if len(unknowns) == 1 {
		return clierr.New(
			clierr.CodeInvalidInput,
			clierr.KindUnknownField,
			"%sunknown field %s%s",
			lead,
			unknowns[0].Detail(),
			tail,
		)
	}
	shown := unknowns
	suffix := ""
	if len(shown) > maxReportedUnknowns {
		shown = shown[:maxReportedUnknowns]
		suffix = fmt.Sprintf("; and %d more", len(unknowns)-maxReportedUnknowns)
	}
	descs := make([]string, 0, len(shown))
	for _, u := range shown {
		descs = append(descs, u.Desc)
	}
	return clierr.New(clierr.CodeInvalidInput, clierr.KindUnknownField, "%s%d unknown fields: %s%s%s",
		lead, len(unknowns), strings.Join(descs, "; "), suffix, tail)
}

// FieldPathKnown reports whether a dotted document path resolves to a real
// field of struct type t. Array indices in the path are tolerated. It is how
// callers ask "is this field known to some *other* schema?" — e.g. whether a
// field rejected by the patch type is a legitimate create-time field.
func FieldPathKnown(t reflect.Type, path string) bool {
	cur := unwrapType(t)
	for seg := range strings.SplitSeq(path, ".") {
		if i := strings.IndexByte(seg, '['); i >= 0 {
			seg = seg[:i]
		}
		if cur == nil {
			return false
		}
		// A map's keys are free-form (environment_variables), so any segment
		// resolves and the walk continues into the value type. Without this
		// a path like container.environment_variables.MY_VAR would look
		// unwritable, which matters because callers really do set it.
		if cur.Kind() == reflect.Map {
			cur = unwrapType(cur.Elem())
			continue
		}
		if cur.Kind() != reflect.Struct {
			return false
		}
		fields := structFields(cur)
		ft, ok := fields[seg]
		if !ok {
			return false
		}
		cur = unwrapType(ft)
		if cur != nil && (cur.Kind() == reflect.Slice || cur.Kind() == reflect.Array) {
			cur = unwrapType(cur.Elem())
		}
	}
	return true
}

// FindFieldPath returns the dotted path at which a field named leaf appears
// in struct type t, or "" when it does not. Used to point a caller at the
// place a field really lives when a request and response disagree on its
// location (create takes container.priority; responses return priority).
func FindFieldPath(t reflect.Type, leaf string) string {
	return findFieldPath(unwrapType(t), leaf, "", 0)
}

func findFieldPath(t reflect.Type, leaf, path string, depth int) string {
	if t == nil || t.Kind() != reflect.Struct || depth > 4 {
		return ""
	}
	fields := structFields(t)
	if _, ok := fields[leaf]; ok {
		return childPath(path, leaf)
	}
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic when several branches could match
	for _, n := range names {
		if found := findFieldPath(unwrapType(fields[n]), leaf, childPath(path, n), depth+1); found != "" {
			return found
		}
	}
	return ""
}

// collectUnknown walks one JSON value against one Go type, appending every
// unknown key to out. path is the dotted location of the value ("" at the
// top level) for the descriptions.
func collectUnknown(data json.RawMessage, t reflect.Type, path string, out *[]Unknown) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return
	}
	t = unwrapType(t)
	if t == nil {
		return // interface{} or unresolvable: free-form by design
	}
	switch t.Kind() {
	case reflect.Struct:
		fields := structFields(t)
		if fields == nil {
			return // no json-tagged fields (e.g. time.Time): leave it be
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(data, &obj); err != nil {
			return // not an object: shape errors are the unmarshaler's job
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic ordering
		for _, k := range keys {
			ft, ok := fields[k]
			if !ok {
				*out = append(*out, describeUnknown(k, path, fields))
				continue // the subtree has no type to check against
			}
			collectUnknown(obj[k], ft, childPath(path, k), out)
		}
	case reflect.Slice, reflect.Array:
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return
		}
		for i, item := range items {
			collectUnknown(item, t.Elem(), fmt.Sprintf("%s[%d]", path, i), out)
		}
	case reflect.Map:
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(data, &obj); err != nil {
			return
		}
		for k, v := range obj { // keys are free-form; values follow the map's value type
			collectUnknown(v, t.Elem(), childPath(path, k), out)
		}
	default:
		// scalars
	}
}

// unwrapType strips pointers and the SDK's Nullable[T] wrapper, returning
// the underlying value type — or nil for interface types (free-form).
func unwrapType(t reflect.Type) reflect.Type {
	for {
		switch {
		case t == nil:
			return nil
		case t.Kind() == reflect.Pointer:
			t = t.Elem()
		case t.Kind() == reflect.Interface:
			return nil
		case isNullable(t):
			f, _ := t.FieldByName("Value")
			t = f.Type
		default:
			return t
		}
	}
}

// isNullable detects the SDK's util.Nullable[T] generic wrapper: a struct
// with exactly the Value/IsNull fields (matched structurally, so this does
// not import the SDK).
func isNullable(t reflect.Type) bool {
	if t.Kind() != reflect.Struct || t.NumField() != 2 {
		return false
	}
	_, hasValue := t.FieldByName("Value")
	isNull, hasIsNull := t.FieldByName("IsNull")
	return hasValue && hasIsNull && isNull.Type.Kind() == reflect.Bool
}

// structFields maps a struct's JSON field names to their Go types,
// flattening anonymous embedded structs. Returns nil when the struct has
// no json-tagged fields at all (an opaque type like time.Time).
func structFields(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	collectFields(t, fields)
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func collectFields(t reflect.Type, out map[string]reflect.Type) {
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous && f.Tag.Get("json") == "" {
			if et := unwrapType(f.Type); et != nil && et.Kind() == reflect.Struct {
				collectFields(et, out)
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = f.Name
		}
		out[name] = f.Type
	}
}

// childPath joins a parent path and a key for error messages.
func childPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// describeUnknown builds the Unknown for one unknown key, with a
// did-you-mean hint when the key is a plausible misspelling of a known
// field at that level. Two checks, in order: swapping underscores and
// hyphens (the API mixes snake_case and kebab-case: scaling_actions vs
// scaling-actions), then a small edit distance (replica → replicas,
// enviroment_variables → environment_variables). The hint must be
// confident — one candidate, within the distance budget — because callers
// withhold the --allow-unknown-fields escape hatch for hinted fields.
//
// cspell:ignore enviroment
func describeUnknown(key, path string, fields map[string]reflect.Type) Unknown {
	where := "at the top level"
	if path != "" {
		where = fmt.Sprintf(`in "%s"`, path)
	}
	hint := suggestField(key, fields)
	suffix := ""
	if hint != "" {
		suffix = fmt.Sprintf(` (did you mean "%s"?)`, hint)
	}
	return Unknown{
		Desc:    fmt.Sprintf(`unknown field "%s" %s%s`, key, where, suffix),
		Path:    childPath(path, key),
		Hint:    hint,
		InArray: strings.Contains(path, "["),
	}
}

// suggestField returns the known field name key most plausibly meant, or
// "". Exact kebab/snake swaps win outright. Otherwise the closest field by
// optimal-string-alignment distance is offered when it is within budget
// (1 for keys of four characters or fewer, else 2) and no other field ties
// it — a tie is not a confident guess.
func suggestField(key string, fields map[string]reflect.Type) string {
	for _, alt := range []string{
		strings.ReplaceAll(key, "_", "-"),
		strings.ReplaceAll(key, "-", "_"),
	} {
		if alt != key {
			if _, ok := fields[alt]; ok {
				return alt
			}
		}
	}
	budget := 2
	if len([]rune(key)) <= 4 {
		budget = 1
	}
	lower := strings.ToLower(key)
	best, bestN := "", 0
	bestD := budget + 1
	for name := range fields {
		d := editDistance(lower, strings.ToLower(name))
		switch {
		case d < bestD:
			best, bestD, bestN = name, d, 1
		case d == bestD:
			bestN++
		}
	}
	if bestD > budget || bestN != 1 {
		return ""
	}
	return best
}

// editDistance is the optimal string alignment (restricted Damerau-
// Levenshtein) distance: insertions, deletions, substitutions and adjacent
// transpositions each cost one.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min3(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				if t := d[i-2][j-2] + 1; t < d[i][j] {
					d[i][j] = t
				}
			}
		}
	}
	return d[len(ra)][len(rb)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// FilterTree returns a copy of the generic JSON tree v with every object key
// that struct type t does not know removed, recursively — arrays are walked
// element by element against the slice's element type, map values against
// the map's value type, and free-form (`any`) fields pass through untouched.
// Every removed key is appended to dropped as its dotted path. It is the
// constructive twin of UnknownFieldList: instead of rejecting a document
// for the fields a schema lacks, it produces the document that schema would
// accept — which is how `get --writable` turns a response into a patch.
func FilterTree(v any, t reflect.Type, path string, dropped *[]string) any {
	t = unwrapType(t)
	if t == nil {
		return v // interface{}: free-form by design
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return v
		}
		fields := structFields(t)
		if fields == nil {
			return v // opaque struct (time.Time): leave it be
		}
		out := make(map[string]any, len(obj))
		for k, child := range obj {
			ft, known := fields[k]
			if !known {
				*dropped = append(*dropped, childPath(path, k))
				continue
			}
			out[k] = FilterTree(child, ft, childPath(path, k), dropped)
		}
		return out
	case reflect.Slice, reflect.Array:
		items, ok := v.([]any)
		if !ok {
			return v
		}
		out := make([]any, len(items))
		for i, item := range items {
			out[i] = FilterTree(item, t.Elem(), fmt.Sprintf("%s[%d]", path, i), dropped)
		}
		return out
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := make(map[string]any, len(obj))
		for k, child := range obj { // keys are free-form; values follow the map's value type
			out[k] = FilterTree(child, t.Elem(), childPath(path, k), dropped)
		}
		return out
	default:
		return v
	}
}
