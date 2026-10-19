package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// The SDK tags every field omitempty, so encoding an SDK value drops every
// unset key. encodeFilled repairs the document on the way out: it walks the Go
// value beside its encoded JSON and adds each key the encoder left out, by
// type — arrays [], maps {}, objects and scalars null. Keys the encoder wrote
// are left alone, so a null the CLI set survives. This is internal/input's
// unknown-field walk run in reverse.

// Marshal encodes v as compact JSON without HTML escaping: the CLI's
// documents are read by people and scripts, never embedded in HTML, so
// "<group>" stays "<group>" rather than "\u003cgroup\u003e".
func Marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// fillMode says which absent keys a walk adds back.
type fillMode int

const (
	// fillAll adds every absent key, by type: the output contract.
	fillAll fillMode = iota
	// fillSetEmpty adds back only a slice or map that is set but empty,
	// which omitempty drops although it is a value the caller chose: a
	// request body must say [] where the user (or the API) said [].
	fillSetEmpty
)

// encodeFilled marshals v compactly with every json-tagged key present.
func encodeFilled(v any) ([]byte, error) {
	return encode(v, fillAll)
}

// MarshalRequest encodes an SDK request value for the wire: [json.Marshal],
// plus every set-but-empty slice or map that the SDK's omitempty tags drop.
// An empty probe headers list is the case that needs it: the API rejects an
// HTTP probe without "headers", and accepts "headers": []. Unset fields stay
// absent; nothing is added that the value did not hold.
func MarshalRequest(v any) ([]byte, error) {
	return encode(v, fillSetEmpty)
}

func encode(v any, mode fillMode) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	doc, err := decodeOrdered(dec)
	if err != nil {
		return nil, err
	}
	return Marshal(fillValue(doc, reflect.ValueOf(v), mode))
}

// object is a JSON object that keeps its key order, so the filled document
// reads in the struct's field order like the encoder's own output.
type object struct {
	keys []string
	vals map[string]any
}

func (o *object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := Marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		vb, err := Marshal(o.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (o *object) has(k string) bool { _, ok := o.vals[k]; return ok }

// errKeyNotString is an object key the JSON decoder did not return as a
// string. It does not happen: JSON keys are strings.
var errKeyNotString = errors.New("is not a string")

// decodeOrdered reads one JSON value, objects as *object and numbers as
// [json.Number] so re-encoding reproduces them exactly.
func decodeOrdered(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if t, ok := tok.(json.Delim); ok {
		switch t {
		case '{':
			o := &object{vals: make(map[string]any)}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key %v %w", kt, errKeyNotString)
				}
				v, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				if !o.has(k) {
					o.keys = append(o.keys, k)
				}
				o.vals[k] = v
			}
			if _, err := dec.Token(); err != nil { // '}'
				return nil, err
			}
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil { // ']'
				return nil, err
			}
			return arr, nil
		}
	}
	return tok, nil
}

var marshalerType = reflect.TypeFor[json.Marshaler]()

// opaque reports whether t encodes itself, so its fields say nothing about
// the document it produces.
func opaque(t reflect.Type) bool {
	return t.Implements(marshalerType) || reflect.PointerTo(t).Implements(marshalerType)
}

// nullableValue returns the index of the Value field when t is the SDK's
// util.Nullable[T] shape (struct{Value T; IsNull bool} that encodes as null
// or as Value), whose document is Value's own.
func nullableValue(t reflect.Type) (int, bool) {
	if t.Kind() != reflect.Struct || t.NumField() != 2 || !opaque(t) {
		return 0, false
	}
	v, ok := t.FieldByName("Value")
	n, nok := t.FieldByName("IsNull")
	if !ok || !nok || n.Type.Kind() != reflect.Bool {
		return 0, false
	}
	return v.Index[0], true
}

// fillValue fills doc, the encoding of v, and returns it.
func fillValue(doc any, v reflect.Value, mode fillMode) any {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return doc
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return doc
	}
	if i, ok := nullableValue(v.Type()); ok {
		if v.FieldByName("IsNull").Bool() {
			return doc
		}
		return fillValue(doc, v.Field(i), mode)
	}
	if opaque(v.Type()) {
		return doc
	}
	switch v.Kind() {
	case reflect.Struct:
		if o, ok := doc.(*object); ok {
			fillStruct(o, v, mode)
		}
	case reflect.Slice, reflect.Array:
		if arr, ok := doc.([]any); ok && v.Len() == len(arr) {
			for i := range arr {
				arr[i] = fillValue(arr[i], v.Index(i), mode)
			}
		}
	case reflect.Map:
		if o, ok := doc.(*object); ok && v.Type().Key().Kind() == reflect.String {
			for _, k := range o.keys {
				mv := v.MapIndex(reflect.ValueOf(k).Convert(v.Type().Key()))
				if mv.IsValid() {
					o.vals[k] = fillValue(o.vals[k], mv, mode)
				}
			}
		}
	}
	return doc
}

// fillStruct fills the keys of struct v in o, in field order: a key the
// encoder wrote keeps its value (filled recursively), an absent one gets
// its type's filler (fillAll) or, when it holds a set-but-empty slice or
// map, [] or {} (fillSetEmpty). Keys o has that no field claims keep their
// place at the end.
func fillStruct(o *object, v reflect.Value, mode fillMode) {
	type field struct {
		name  string
		depth int
		typ   reflect.Type
		val   reflect.Value // invalid under a nil embedded pointer
	}
	var fields []field
	var walk func(v reflect.Value, t reflect.Type, depth int)
	walk = func(v reflect.Value, t reflect.Type, depth int) {
		for i := range t.NumField() {
			f := t.Field(i)
			name, ok := jsonName(f)
			if !ok {
				continue
			}
			var fv reflect.Value
			if v.IsValid() {
				fv = v.Field(i)
			}
			if name == "" { // embedded struct: its fields are promoted
				ft := f.Type
				if ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
					if fv.IsValid() {
						if fv.IsNil() {
							fv = reflect.Value{}
						} else {
							fv = fv.Elem()
						}
					}
				}
				walk(fv, ft, depth+1)
				continue
			}
			fields = append(fields, field{name, depth, f.Type, fv})
		}
	}
	walk(v, v.Type(), 0)

	// As in encoding/json, the shallowest field of a name wins.
	best := make(map[string]field)
	for _, f := range fields {
		if b, ok := best[f.name]; !ok || f.depth < b.depth {
			best[f.name] = f
		}
	}
	var keys []string
	for _, f := range fields {
		b, ok := best[f.name]
		if !ok || b.depth != f.depth {
			continue
		}
		delete(best, f.name)
		switch {
		case !o.has(f.name) && mode == fillAll:
			o.vals[f.name] = filler(f.typ)
		case !o.has(f.name):
			e, ok := setEmpty(f.val)
			if !ok {
				continue // unset: stays absent, and keeps no place in the order
			}
			o.vals[f.name] = e
		case f.val.IsValid():
			o.vals[f.name] = fillValue(o.vals[f.name], f.val, mode)
		}
		keys = append(keys, f.name)
	}
	claimed := make(map[string]bool)
	for _, k := range keys {
		claimed[k] = true
	}
	for _, k := range o.keys {
		if !claimed[k] {
			keys = append(keys, k)
		}
	}
	o.keys = keys
}

// jsonName returns the key encoding/json uses for f, "" for an embedded
// struct whose fields are promoted, and false for a field it never writes.
func jsonName(f reflect.StructField) (string, bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", false
	}
	name, _, _ := strings.Cut(tag, ",")
	if f.Anonymous && name == "" {
		t := f.Type
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() == reflect.Struct && !opaque(t) {
			return "", true
		}
	}
	if !f.IsExported() {
		return "", false
	}
	if name == "" {
		name = f.Name
	}
	return name, true
}

// setEmpty reports whether v (through pointers) is a non-nil slice or map
// with no elements, and returns its document, [] or {}. A nil slice or map
// is unset, not empty.
func setEmpty(v reflect.Value) (any, bool) {
	for v.IsValid() && v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, false
		}
		v = v.Elem()
	}
	if !v.IsValid() || opaque(v.Type()) {
		return nil, false
	}
	switch v.Kind() {
	case reflect.Slice:
		if v.IsNil() || v.Len() > 0 || v.Type().Elem().Kind() == reflect.Uint8 {
			return nil, false
		}
		return []any{}, true
	case reflect.Map:
		if v.IsNil() || v.Len() > 0 {
			return nil, false
		}
		return &object{vals: make(map[string]any)}, true
	}
	return nil, false
}

// filler is the value an absent key of type t renders as: [] for an array,
// {} for a map, null for anything else.
func filler(t reflect.Type) any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if opaque(t) {
		return nil // also an absent Nullable: it would have encoded as null or a value
	}
	switch t.Kind() {
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 { // []byte encodes as a string
			return nil
		}
		return []any{}
	case reflect.Array:
		return []any{}
	case reflect.Map:
		return &object{vals: make(map[string]any)}
	}
	return nil
}
