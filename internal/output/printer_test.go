package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

type row struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// capture returns a Printer that renders into the returned buffer.
func capture(t *testing.T) (*Printer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return NewPrinter(&buf), &buf
}

func TestSetMode(t *testing.T) {
	t.Parallel()
	p, _ := capture(t)

	if err := p.SetMode("json"); err != nil {
		t.Fatalf(`p.SetMode("json") error: %v`, err)
	}
	if p.Mode() != "json" {
		t.Errorf("p.Mode() = %q, want json", p.Mode())
	}
	if err := p.SetMode("table"); err != nil {
		t.Fatalf(`p.SetMode("table") error: %v`, err)
	}
	if p.Mode() != "table" {
		t.Errorf("p.Mode() = %q, want table", p.Mode())
	}

	err := p.SetMode("bogus")
	if err == nil {
		t.Fatal(`p.SetMode("bogus"): want error, got nil`)
	}
	var ce *clierr.Error
	ok := errors.As(err, &ce)
	if !ok {
		t.Fatalf(`p.SetMode("bogus") error type = %T, want *clierr.Error`, err)
	}
	if ce.Code != clierr.CodeUsage || ce.Kind != clierr.KindUsage {
		t.Errorf("SetMode error = code %d kind %q, want code %d kind usage", ce.Code, ce.Kind, clierr.CodeUsage)
	}
	if p.Mode() != "table" {
		t.Errorf("p.Mode() after failed SetMode = %q, want table unchanged", p.Mode())
	}
}

func TestEmitJSON(t *testing.T) {
	t.Parallel()
	p, buf := capture(t)
	if err := p.SetMode("json"); err != nil {
		t.Fatal(err)
	}

	v := row{Name: "my-group", State: "running"}
	if err := p.Emit(v, func(_ *Writer) { t.Error("table func must not be called in json mode") }); err != nil {
		t.Fatalf("Emit error: %v", err)
	}

	var got row
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if got != v {
		t.Errorf("round trip = %+v, want %+v", got, v)
	}
	if !strings.Contains(buf.String(), "\n  ") {
		t.Errorf("json mode should be indented, got %q", buf.String())
	}
}

func TestEmitTable(t *testing.T) {
	t.Parallel()
	p, buf := capture(t)
	// Default mode is table (capture's cleanup guarantees the reset).
	v := row{Name: "my-group", State: "running"}
	err := p.Emit(v, func(w *Writer) {
		w.Print("NAME\tSTATE\n")
		w.Printf("%s\t%s\n", v.Name, v.State)
	})
	if err != nil {
		t.Fatalf("Emit error: %v", err)
	}
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("table output = %d lines, want 2:\n%s", len(lines), out)
	}
	// tabwriter aligns columns: STATE starts at the same offset on both lines.
	if strings.Index(lines[0], "STATE") != strings.Index(lines[1], "running") {
		t.Errorf("columns not aligned:\n%s", out)
	}
	if strings.Contains(out, "\t") {
		t.Errorf("table output still contains raw tabs:\n%q", out)
	}
}

func TestStreamJSON(t *testing.T) {
	t.Parallel()
	p, buf := capture(t)
	if err := p.SetMode("json"); err != nil {
		t.Fatal(err)
	}

	for _, v := range []row{{"a", "running"}, {"b", "stopped"}} {
		if err := p.Stream(v, "ignored in json mode"); err != nil {
			t.Fatalf("Stream error: %v", err)
		}
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("NDJSON output = %d lines, want 2:\n%s", len(lines), buf.String())
	}
	for i, line := range lines {
		if strings.ContainsAny(line, " \t") && strings.Contains(line, ": ") {
			t.Errorf("line %d is not compact JSON: %q", i, line)
		}
		var got row
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Errorf("line %d is not valid JSON: %v (%q)", i, err, line)
		}
	}
}

func TestStreamTable(t *testing.T) {
	t.Parallel()
	p, buf := capture(t)
	if err := p.Stream(row{"a", "running"}, "2026-01-01 my log line"); err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if got, want := buf.String(), "2026-01-01 my log line\n"; got != want {
		t.Errorf("Stream table output = %q, want %q", got, want)
	}
}

func TestEmitYAML(t *testing.T) {
	t.Parallel()
	p, buf := capture(t)
	if err := p.SetMode("yaml"); err != nil {
		t.Fatal(err)
	}
	if err := p.Emit(row{Name: "my-group", State: "running"}, nil); err != nil {
		t.Fatal(err)
	}
	if want := "name: my-group\nstate: running\n"; buf.String() != want {
		t.Errorf("Emit yaml = %q, want %q", buf.String(), want)
	}
}

// Stream in yaml mode is a YAML stream: each record its own document,
// opened with ---.
func TestStreamYAML(t *testing.T) {
	t.Parallel()
	p, buf := capture(t)
	if err := p.SetMode("yaml"); err != nil {
		t.Fatal(err)
	}
	for _, v := range []row{{"a", "running"}, {"b", "stopped"}} {
		if err := p.Stream(v, ""); err != nil {
			t.Fatal(err)
		}
	}
	if want := "---\nname: a\nstate: running\n---\nname: b\nstate: stopped\n"; buf.String() != want {
		t.Errorf("Stream yaml = %q, want %q", buf.String(), want)
	}
}

// TestJSONIsASCII: json output (Emit and Stream) escapes every non-ASCII
// character, in keys and values, and decodes to the same document; yaml
// keeps the characters as they are.
func TestJSONIsASCII(t *testing.T) {
	t.Parallel()
	doc := map[string]any{"name": "Café 東京", "emoji": "🎉 ok", "Größe": "ü", "plain": "<a&b>"}
	check := func(t *testing.T, out []byte) {
		t.Helper()
		for i, b := range out {
			if b >= 0x80 {
				t.Fatalf("byte %d is not ASCII: %q", i, out)
			}
		}
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(got) != fmt.Sprint(doc) {
			t.Errorf("decoded %v, want %v", got, doc)
		}
		if !strings.Contains(string(out), "<a&b>") {
			t.Errorf("ASCII escaped an ASCII character: %s", out)
		}
	}
	for _, stream := range []bool{false, true} {
		p, buf := capture(t)
		if err := p.SetMode("json"); err != nil {
			t.Fatal(err)
		}
		var err error
		if stream {
			err = p.Stream(doc, "")
		} else {
			err = p.Emit(doc, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		check(t, buf.Bytes())
	}
	p, buf := capture(t)
	if err := p.SetMode("yaml"); err != nil {
		t.Fatal(err)
	}
	if err := p.Emit(doc, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Café 東京") || !strings.Contains(buf.String(), "ü") {
		t.Errorf("yaml changed the characters:\n%s", buf.String())
	}
	bs := string(rune(92)) // a backslash
	if got, want := string(ASCII([]byte(`"🎉"`))), `"`+bs+`ud83c`+bs+`udf89"`; got != want {
		t.Errorf("ASCII = %s, want the surrogate pair %s", got, want)
	}
}

// must returns v and fails the test when err is set, on the test goroutine:
// must(os.ReadFile(p))(t).
func must[T any](v T, err error) func(*testing.T) T {
	return func(t *testing.T) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}
