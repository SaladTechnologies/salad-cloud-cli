// Package input loads request payloads from JSON or YAML definition files.
package input

import (
	"encoding/json"
	"io"
	"os"
	"reflect"

	"sigs.k8s.io/yaml"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// read returns the bytes of path (or stdin when path == "-") and the name
// to use for it in error messages.
func read(path string) (name string, data []byte, cerr *clierr.Error) {
	name = path
	var err error
	if path == "-" {
		name = "stdin"
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return name, nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "cannot read %s: %v", name, err)
	}
	return name, data, nil
}

// loadFile is the shared body of Load, LoadRaw, and LoadLenient: read,
// strict-unmarshal into out, convert to JSON, and walk the document for
// fields out's struct type does not know. In strict mode (Load/LoadRaw) the
// first unknown field is a clierr.KindUnknownField error; in lenient mode
// (LoadLenient) they are all returned for the caller to warn about.
//
// The unknown-field walk exists because yaml.UnmarshalStrict cannot do the
// job alone: the SDK's request structs implement [json.Unmarshaler] (for
// Nullable fields), and a custom unmarshaler opts a type out of strict field
// checking entirely — so without the walk, every typo'd field is a silent
// no-op (see strict.go).
func loadFile(path string, out any, strict bool) (json.RawMessage, []Unknown, *clierr.Error) {
	name, data, cerr := read(path)
	if cerr != nil {
		return nil, nil, cerr
	}
	if err := yaml.UnmarshalStrict(data, out); err != nil {
		return nil, nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "cannot parse %s: %v", name, err)
	}
	raw, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "cannot parse %s: %v", name, err)
	}
	var unknowns []Unknown
	if t := reflect.TypeOf(out); t != nil && t.Kind() == reflect.Pointer {
		unknowns = UnknownFieldList(raw, t.Elem())
	}
	if strict && len(unknowns) > 0 {
		return nil, nil, clierr.New(
			clierr.CodeInvalidInput,
			clierr.KindUnknownField,
			"cannot use %s: unknown field %s",
			name,
			unknowns[0].Detail(),
		)
	}
	return raw, unknowns, nil
}

// Load reads path (or stdin when path == "-"), converts YAML to JSON via
// sigs.k8s.io/yaml, and unmarshals into out (the SDK structs carry json
// tags, so both .json and .yaml/.yml files work — JSON is valid YAML).
// Unknown fields anywhere in the document are rejected with a
// CodeInvalidInput error naming the offending path (fields typed `any`,
// like an inference job's input, stay free-form).
func Load(path string, out any) *clierr.Error {
	_, _, cerr := loadFile(path, out, true)
	return cerr
}

// LoadRaw is Load plus the converted JSON document itself — the exact bytes
// a raw passthrough will put on the wire. The struct unmarshal is the same
// strict validation pass as Load's; the returned bytes are the document as
// the user wrote it (YAML converted to JSON), NOT a re-marshal of out.
// That distinction matters for merge patches: re-marshaling through the
// SDK's omitempty-tagged structs drops explicit empty values ([], "", 0),
// which in merge-patch semantics mean "clear this field".
func LoadRaw(path string, out any) (json.RawMessage, *clierr.Error) {
	raw, _, cerr := loadFile(path, out, true)
	return raw, cerr
}

// ReadDocument reads path (or stdin when path == "-") and converts it from
// YAML to JSON without unmarshaling it into a type, returning the name to use
// for it in error messages. It is for callers that check fields themselves
// before unmarshaling, so the check runs even for types with no custom
// unmarshaler (which UnmarshalStrict would reject first, without a path).
func ReadDocument(path string) (name string, raw json.RawMessage, cerr *clierr.Error) {
	name, data, cerr := read(path)
	if cerr != nil {
		return name, nil, cerr
	}
	// Strict parse first: YAMLToJSON keeps the last of a duplicated key
	// silently, which Load (UnmarshalStrict) rejects.
	var doc any
	if err := yaml.UnmarshalStrict(data, &doc); err != nil {
		return name, nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "cannot parse %s: %v", name, err)
	}
	raw, err := yaml.YAMLToJSON(data)
	if err != nil {
		return name, nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "cannot parse %s: %v", name, err)
	}
	return name, raw, nil
}

// LoadLenient is LoadRaw with unknown fields tolerated instead of rejected:
// they come back as the Unknown list for the caller to warn about, and the
// raw document (which still carries them) is what goes on the wire. This is
// the --allow-unknown-fields path — the forward-compat escape hatch for API
// fields newer than this CLI's pinned SDK model.
func LoadLenient(path string, out any) (json.RawMessage, []Unknown, *clierr.Error) {
	return loadFile(path, out, false)
}
