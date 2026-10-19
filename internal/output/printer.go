// Package output owns the CLI's single render path: table (human), json or
// yaml (machine) mode, selected once via the global --output flag. yaml is
// the json document, key fill included, re-encoded.
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"sigs.k8s.io/yaml"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// The output modes.
const (
	modeTable = "table"
	modeJSON  = "json"
	modeYAML  = "yaml"
)

// escapeHeadroom is the room ASCII leaves for escapes beyond the document's own length before append must grow.
const escapeHeadroom = 16

// Printer is one run's render path: where rendered output goes and the mode
// it is rendered in, table (the default), json or yaml.
type Printer struct {
	w    io.Writer
	mode string
}

// NewPrinter returns a Printer writing to w in table mode.
func NewPrinter(w io.Writer) *Printer { return &Printer{w: w, mode: modeTable} }

// Out is where p writes.
func (p *Printer) Out() io.Writer { return p.w }

// SetMode selects the output mode: "table" (default), "json", or "yaml".
// Any other value returns a usage error and leaves the mode as it was.
func (p *Printer) SetMode(m string) *clierr.Error {
	switch m {
	case modeTable, modeJSON, modeYAML:
		p.mode = m
		return nil
	default:
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, `invalid --output "%s"`, m).
			WithHint("use table, json or yaml")
	}
}

// Machine reports whether the mode is a document mode (json or yaml), the
// modes errors are reported in as a JSON document on stderr.
func (p *Printer) Machine() bool { return p.mode != modeTable }

// Mode returns the current output mode.
func (p *Printer) Mode() string { return p.mode }

// Emit is the single render path for non-streaming commands.
// In json mode it writes v indented to p's writer, with every json-tagged key
// present (see encodeFilled) and every non-ASCII character escaped (ASCII);
// yaml mode writes the same document as YAML, characters as they are.
// In table mode it calls table with a Writer over a tabwriter that Emit
// flushes afterward, and returns the first write error.
func (p *Printer) Emit(v any, table func(w *Writer)) error {
	switch p.mode {
	case modeYAML:
		return p.emitYAML(v)
	case modeJSON:
		return p.emitJSON(v)
	}
	return p.emitTable(table)
}

// Stream renders one streamed record (e.g. a log line) and flushes
// immediately so pipes stream. In json mode it writes v as one compact
// line (NDJSON) with every json-tagged key present, as Emit does; in table
// mode it prints line as-is.
func (p *Printer) Stream(v any, line string) error {
	if p.mode == modeYAML {
		// A YAML stream: one document per record, each opened with ---.
		data, err := encodeFilled(v)
		if err != nil {
			return err
		}
		y, err := yaml.JSONToYAML(data)
		if err != nil {
			return fmt.Errorf("converting the document to YAML: %w", err)
		}
		if _, err := fmt.Fprintf(p.w, "---\n%s", y); err != nil {
			return fmt.Errorf("writing the record: %w", err)
		}
		return flush(p.w)
	}
	if p.mode == modeJSON {
		data, err := encodeFilled(v)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(p.w, string(ASCII(data))); err != nil {
			return fmt.Errorf("writing the record: %w", err)
		}
	} else {
		if _, err := fmt.Fprintln(p.w, line); err != nil {
			return fmt.Errorf("writing the line: %w", err)
		}
	}
	return flush(p.w)
}

// emitYAML is Emit in yaml mode.
func (p *Printer) emitYAML(v any) error {
	data, err := encodeFilled(v)
	if err != nil {
		return err
	}
	y, err := yaml.JSONToYAML(data)
	if err != nil {
		return fmt.Errorf("converting the document to YAML: %w", err)
	}
	if _, err = p.w.Write(y); err != nil {
		return fmt.Errorf("writing the document: %w", err)
	}
	return nil
}

// emitJSON is Emit in json mode.
func (p *Printer) emitJSON(v any) error {
	data, err := encodeFilled(v)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err = json.Indent(&buf, ASCII(data), "", "  "); err != nil {
		return fmt.Errorf("indenting the document: %w", err)
	}
	if _, err = fmt.Fprintln(p.w, buf.String()); err != nil {
		return fmt.Errorf("writing the document: %w", err)
	}
	return nil
}

// emitTable is Emit in table mode.
func (p *Printer) emitTable(table func(w *Writer)) error {
	tw := newTabWriter(p.w)
	w := NewWriter(tw)
	table(w)
	if err := tw.Flush(); w.err == nil {
		w.err = err
	}
	if w.err != nil {
		return fmt.Errorf("writing the table: %w", w.err)
	}
	return nil
}

// flush pushes buffered bytes through if the writer supports it.
// [os.Stdout] is unbuffered in Go, so there is nothing to do for it.
func flush(w io.Writer) error {
	if f, ok := w.(interface{ Flush() error }); ok {
		if err := f.Flush(); err != nil {
			return fmt.Errorf("flushing the output: %w", err)
		}
	}
	return nil
}

// ASCII rewrites every non-ASCII character in a JSON document as a \uXXXX
// escape (a surrogate pair above U+FFFF), so the document is plain ASCII and
// decodes to the same value. Windows PowerShell decodes a native command's
// output in the console's code page, which garbles UTF-8; an escape survives
// any code page. In valid JSON a non-ASCII byte can only be inside a string,
// where the escape means the same character.
func ASCII(doc []byte) []byte {
	i := 0
	for i < len(doc) && doc[i] < utf8.RuneSelf {
		i++
	}
	if i == len(doc) {
		return doc
	}
	out := make([]byte, 0, len(doc)+escapeHeadroom)
	out = append(out, doc[:i]...)
	for i < len(doc) {
		if doc[i] < utf8.RuneSelf {
			out = append(out, doc[i])
			i++
			continue
		}
		r, size := utf8.DecodeRune(doc[i:])
		i += size
		if hi, lo := utf16.EncodeRune(r); hi != unicode.ReplacementChar { // outside the BMP: a surrogate pair
			out = fmt.Appendf(out, `\u%04x\u%04x`, hi, lo)
			continue
		}
		out = fmt.Appendf(out, `\u%04x`, r)
	}
	return out
}
