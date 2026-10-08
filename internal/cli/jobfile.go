package cli

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/input"
)

// jobFileShapeHint follows an unknown-field error when a job file has no
// "input" key: the commonest mistake is writing the input document alone,
// which --input takes but -f does not.
const jobFileShapeHint = `put your fields under "input", or pass them with --input '<json>'; ` +
	`a job file is the whole job, {"input": {...}, "metadata"?, "webhook"?}`

// loadJobFile loads a job prototype file (-f) for `queues jobs create` and
// `inference jobs create` into out (a pointer to the prototype). Fields are
// checked before unmarshaling: the prototypes have no custom unmarshaler, so
// a strict unmarshal would reject an unknown field first, as a parse error
// with no hint. An unknown field is kind unknown_field, and gets
// jobFileShapeHint when the file has unknown top-level fields and no "input"
// key.
func loadJobFile(file string, out any) *clierr.Error {
	name, raw, cerr := input.ReadDocument(file)
	if cerr != nil {
		return cerr
	}
	if unknowns := input.UnknownFieldList(raw, reflect.TypeOf(out).Elem()); len(unknowns) > 0 {
		e := input.UnknownFieldsError("cannot use "+name+": ", unknowns, "")
		var doc map[string]json.RawMessage
		if json.Unmarshal(raw, &doc) == nil {
			if _, ok := doc["input"]; !ok && hasTopLevelUnknown(unknowns) {
				e.WithHint("%s", jobFileShapeHint)
			}
		}
		return e
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "cannot parse %s: %v", name, err)
	}
	return nil
}

// hasTopLevelUnknown reports whether any unknown field sits at the top level
// of the document.
func hasTopLevelUnknown(unknowns []input.Unknown) bool {
	for _, u := range unknowns {
		if !strings.ContainsAny(u.Path, ".[") {
			return true
		}
	}
	return false
}
