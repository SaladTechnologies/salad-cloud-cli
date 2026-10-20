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
func read(path string, stdin io.Reader) (string, []byte, *clierr.Error) {
	name := path
	var data []byte
	var err error
	if path == "-" {
		name = "stdin"
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(path) //nolint:gosec // reading the file the user named is the point
	}
	if err != nil {
		return name, nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "cannot read %s: %v", name, err)
	}
	return name, data, nil
}

// loaded is a definition file loadFile read: the name to use for it in error messages, the document as JSON, and
// the fields in it that the target type does not know.
type loaded struct {
	name     string
	raw      json.RawMessage
	unknowns []Unknown
}

// rejectUnknown is the clierr.KindUnknownField error for the document's first unknown field, or nil when it has
// none: strict mode (Load/LoadRaw).
func (l *loaded) rejectUnknown() *clierr.Error {
	if len(l.unknowns) == 0 {
		return nil
	}
	return clierr.New(
		clierr.CodeInvalidInput,
		clierr.KindUnknownField,
		"cannot use %s: unknown field %s",
		l.name,
		l.unknowns[0].Detail(),
	)
}

// loadFile is the shared body of Load, LoadRaw, and LoadLenient: read,
// strict-unmarshal into out, convert to JSON, and walk the document for
// fields out's struct type does not know. Strict mode (Load/LoadRaw) rejects
// the first unknown field with rejectUnknown; lenient mode (LoadLenient)
// returns them all for the caller to warn about.
//
// The unknown-field walk exists because yaml.UnmarshalStrict cannot do the
// job alone: the SDK's request structs implement [json.Unmarshaler] (for
// Nullable fields), and a custom unmarshaler opts a type out of strict field
// checking entirely — so without the walk, every typo'd field is a silent
// no-op (see strict.go).
func loadFile(path string, stdin io.Reader, out any) (*loaded, *clierr.Error) {
	name, data, cerr := read(path, stdin)
	if cerr != nil {
		return nil, cerr
	}
	if err := yaml.UnmarshalStrict(data, out); err != nil {
		return nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "cannot parse %s: %v", name, err)
	}
	raw, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "cannot parse %s: %v", name, err)
	}
	var unknowns []Unknown
	if t := reflect.TypeOf(out); t != nil && t.Kind() == reflect.Pointer {
		unknowns = UnknownFieldList(raw, t.Elem())
	}
	return &loaded{name: name, raw: raw, unknowns: unknowns}, nil
}

// Load reads path (or stdin when path == "-"), converts YAML to JSON via
// sigs.k8s.io/yaml, and unmarshals into out (the SDK structs carry json
// tags, so both .json and .yaml/.yml files work — JSON is valid YAML).
// Unknown fields anywhere in the document are rejected with a
// CodeInvalidInput error naming the offending path (fields typed `any`,
// like an inference job's input, stay free-form).
func Load(path string, stdin io.Reader, out any) *clierr.Error {
	l, cerr := loadFile(path, stdin, out)
	if cerr != nil {
		return cerr
	}
	return l.rejectUnknown()
}

// LoadRaw is Load plus the converted JSON document itself — the exact bytes
// a raw passthrough will put on the wire. The struct unmarshal is the same
// strict validation pass as Load's; the returned bytes are the document as
// the user wrote it (YAML converted to JSON), NOT a re-marshal of out.
// That distinction matters for merge patches: re-marshaling through the
// SDK's omitempty-tagged structs drops explicit empty values ([], "", 0),
// which in merge-patch semantics mean "clear this field".
func LoadRaw(path string, stdin io.Reader, out any) (json.RawMessage, *clierr.Error) {
	l, cerr := loadFile(path, stdin, out)
	if cerr != nil {
		return nil, cerr
	}
	if cerr := l.rejectUnknown(); cerr != nil {
		return nil, cerr
	}
	return l.raw, nil
}

// ReadDocument reads path (or stdin when path == "-") and converts it from
// YAML to JSON without unmarshaling it into a type, returning the name to use
// for it in error messages. It is for callers that check fields themselves
// before unmarshaling, so the check runs even for types with no custom
// unmarshaler (which UnmarshalStrict would reject first, without a path).
func ReadDocument(path string, stdin io.Reader) (string, json.RawMessage, *clierr.Error) {
	name, data, cerr := read(path, stdin)
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
func LoadLenient(path string, stdin io.Reader, out any) (json.RawMessage, []Unknown, *clierr.Error) {
	l, cerr := loadFile(path, stdin, out)
	if cerr != nil {
		return nil, nil, cerr
	}
	return l.raw, l.unknowns, nil
}
