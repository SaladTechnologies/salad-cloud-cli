package output

import (
	"encoding/json"
	"strings"
	"testing"
)

type fillInner struct {
	ID   *string `json:"id,omitempty"`
	Tags []int   `json:"tags,omitempty"`
}

type fillBase struct {
	Base  *string `json:"base,omitempty"`
	Shady *string `json:"shady,omitempty"` // shadowed by fillOuter.Shady
}

// selfEncoded encodes itself, so its fields say nothing about the document.
type selfEncoded struct{ Hidden *string }

func (selfEncoded) MarshalJSON() ([]byte, error) { return []byte(`"custom"`), nil }

type fillOuter struct {
	fillBase
	Name    *string           `json:"name,omitempty"`
	Count   int               `json:"count,omitempty"`
	List    []fillInner       `json:"list,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
	Nested  *fillInner        `json:"nested,omitempty"`
	ByKey   map[string]fillInner
	Any     any          `json:"any,omitempty"`
	Custom  *selfEncoded `json:"custom,omitempty"`
	Bytes   []byte       `json:"bytes,omitempty"`
	Null    *string      `json:"null"`
	Skipped string       `json:"-"`
	Shady   *string      `json:"shady,omitempty"`
	private string
}

func fill(t *testing.T, v any) string {
	t.Helper()
	data, err := encodeFilled(v)
	if err != nil {
		t.Fatalf("encodeFilled: %v", err)
	}
	return string(data)
}

func TestFillZeroValue(t *testing.T) {
	got := fill(t, fillOuter{})
	want := `{"base":null,"name":null,"count":null,"list":[],"labels":{},"nested":null,"ByKey":null,` +
		`"any":null,"custom":null,"bytes":null,"null":null,"shady":null}`
	if got != want {
		t.Errorf("zero value\n got %s\nwant %s", got, want)
	}
}

func TestFillRecursesAndKeepsWrittenKeys(t *testing.T) {
	s := "x"
	v := fillOuter{
		Name:   &s,
		Count:  3,
		List:   []fillInner{{ID: &s}, {}},
		Nested: &fillInner{Tags: []int{1}},
		ByKey:  map[string]fillInner{"k": {}},
		Any:    fillInner{},
		Custom: &selfEncoded{},
	}
	got := fill(t, v)
	want := `{"base":null,"name":"x","count":3,"list":[{"id":"x","tags":[]},{"id":null,"tags":[]}],"labels":{},` +
		`"nested":{"id":null,"tags":[1]},"ByKey":{"k":{"id":null,"tags":[]}},"any":{"id":null,"tags":[]},` +
		`"custom":"custom","bytes":null,"null":null,"shady":null}`
	if got != want {
		t.Errorf("filled\n got %s\nwant %s", got, want)
	}
}

// A null or empty value the CLI wrote on purpose is not replaced.
func TestFillLeavesExplicitValues(t *testing.T) {
	type own struct {
		Unknown []string          `json:"unknown"`
		Empty   []string          `json:"empty"`
		Map     map[string]string `json:"map"`
	}
	got := fill(t, own{Empty: []string{}})
	if want := `{"unknown":null,"empty":[],"map":null}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestFillPreservesNumbersAndEscapes(t *testing.T) {
	type nums struct {
		Big  int64   `json:"big"`
		F    float64 `json:"f"`
		Text string  `json:"text"`
	}
	v := nums{Big: 9007199254740993, F: 0.1, Text: "<a&b>"}
	plain, _ := Marshal(v)
	if got := fill(t, v); got != string(plain) {
		t.Errorf("got %s, want %s (the encoder's own bytes)", got, plain)
	}
	// No HTML escaping: a placeholder in a message reads as typed.
	if !strings.Contains(string(plain), `"<a&b>"`) {
		t.Errorf("Marshal escaped HTML: %s", plain)
	}
}

func TestFillTopLevelSlice(t *testing.T) {
	got := fill(t, []fillInner{{}})
	if want := `[{"id":null,"tags":[]}]`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got := fill(t, []fillInner(nil)); got != "null" {
		t.Errorf("nil slice = %s, want null", got)
	}
}

func TestEmitAndStreamFill(t *testing.T) {
	buf := capture(t)
	if err := SetMode("json"); err != nil {
		t.Fatal(err)
	}
	if err := Emit(fillInner{}, nil); err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"id\": null,\n  \"tags\": []\n}\n"; buf.String() != want {
		t.Errorf("Emit = %q, want %q", buf.String(), want)
	}
	buf.Reset()
	if err := Stream(fillInner{}, ""); err != nil {
		t.Fatal(err)
	}
	if want := `{"id":null,"tags":[]}` + "\n"; buf.String() != want {
		t.Errorf("Stream = %q, want %q", buf.String(), want)
	}
	if strings.Count(buf.String(), "\n") != 1 {
		t.Errorf("Stream must write one line, got %q", buf.String())
	}
}

// nullable mirrors the SDK's util.Nullable[T]: it encodes as null or as
// Value, so the fill reaches through it into Value's document.
type nullable[T any] struct {
	Value  T
	IsNull bool
}

func (n *nullable[T]) MarshalJSON() ([]byte, error) {
	if n.IsNull {
		return []byte("null"), nil
	}
	return json.Marshal(n.Value)
}

func TestFillReachesThroughNullable(t *testing.T) {
	type probe struct {
		Probe *nullable[fillInner] `json:"probe,omitempty"`
		Null  *nullable[fillInner] `json:"null,omitempty"`
		Gone  *nullable[fillInner] `json:"gone,omitempty"`
	}
	got := fill(t, probe{Probe: &nullable[fillInner]{}, Null: &nullable[fillInner]{IsNull: true}})
	if want := `{"probe":{"id":null,"tags":[]},"null":null,"gone":null}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// MarshalRequest keeps the empty lists and maps a value set, which omitempty
// drops, and adds nothing the value did not hold.
func TestMarshalRequestKeepsSetEmpties(t *testing.T) {
	enc := func(v any) string {
		t.Helper()
		data, err := MarshalRequest(v)
		if err != nil {
			t.Fatalf("MarshalRequest: %v", err)
		}
		return string(data)
	}
	if got := enc(fillOuter{}); got != `{"ByKey":null,"null":null}` {
		t.Errorf("zero value = %s, want only the keys the encoder writes itself", got)
	}
	s := "x"
	v := fillOuter{
		Name:   &s,
		List:   []fillInner{{Tags: []int{}}, {ID: &s}},
		Labels: map[string]string{},
		Nested: &fillInner{Tags: []int{}},
		ByKey:  map[string]fillInner{"k": {Tags: []int{}}},
		Bytes:  []byte{},
		Custom: &selfEncoded{Hidden: &s},
	}
	want := `{"name":"x","list":[{"tags":[]},{"id":"x"}],"labels":{},"nested":{"tags":[]},` +
		`"ByKey":{"k":{"tags":[]}},"custom":"custom","null":null}`
	if got := enc(v); got != want {
		t.Errorf("set empties\n got %s\nwant %s", got, want)
	}
	type probe struct {
		Probe *nullable[fillInner] `json:"probe,omitempty"`
		Null  *nullable[fillInner] `json:"null,omitempty"`
	}
	got := enc(probe{Probe: &nullable[fillInner]{Value: fillInner{Tags: []int{}}}, Null: &nullable[fillInner]{IsNull: true}})
	if want := `{"probe":{"tags":[]},"null":null}`; got != want {
		t.Errorf("through Nullable = %s, want %s", got, want)
	}
}
