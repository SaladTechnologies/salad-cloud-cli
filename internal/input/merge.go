package input

import (
	"encoding/json"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// MergeJSON deep-merges the overlay document onto the base document and
// returns the result: objects merge key-by-key recursively, and for
// everything else (scalars, arrays, nulls) the overlay value wins.
//
// It exists for create --allow-unknown-fields: the built request struct
// (flags + defaults, marshaled) is the overlay, the user's raw definition is
// the base, so unknown fields the struct round-trip would drop survive into
// the outgoing request while every flag override still wins. The array rule
// is why unknown fields INSIDE array elements cannot be preserved on create:
// the struct's round-tripped array replaces the base's wholesale.
func MergeJSON(base, overlay json.RawMessage) (json.RawMessage, *clierr.Error) {
	var b, o any
	if err := json.Unmarshal(base, &b); err != nil {
		return nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "cannot merge documents: %v", err)
	}
	if err := json.Unmarshal(overlay, &o); err != nil {
		return nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "cannot merge documents: %v", err)
	}
	merged, err := json.Marshal(mergeValues(b, o))
	if err != nil {
		return nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "cannot merge documents: %v", err)
	}
	return merged, nil
}

// mergeValues merges overlay onto base: maps recurse, anything else the
// overlay wins outright.
func mergeValues(base, overlay any) any {
	bm, bok := base.(map[string]any)
	om, ook := overlay.(map[string]any)
	if !bok || !ook {
		return overlay
	}
	out := make(map[string]any, len(bm)+len(om))
	for k, v := range bm {
		out[k] = v
	}
	for k, ov := range om {
		if bv, exists := out[k]; exists {
			out[k] = mergeValues(bv, ov)
		} else {
			out[k] = ov
		}
	}
	return out
}
