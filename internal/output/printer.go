// Package output owns the CLI's single render path: table (human), json or
// yaml (machine) mode, selected once via the global --output flag. yaml is
// the json document, key fill included, re-encoded.
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"unicode/utf16"
	"unicode/utf8"

	"sigs.k8s.io/yaml"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// Stdout is the destination for all rendered output. Tests may swap it.
var Stdout io.Writer = os.Stdout

var mode = "table"

// SetMode selects the output mode: "table" (default), "json", or "yaml".
// Any other value returns a usage error.
func SetMode(m string) error {
	switch m {
	case "table", "json", "yaml":
		mode = m
		return nil
	default:
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, `invalid --output "%s"`, m).
			WithHint("use table, json or yaml")
	}
}

// Machine reports whether the mode is a document mode (json or yaml), the
// modes errors are reported in as a JSON document on stderr.
func Machine() bool { return mode != "table" }

// Mode returns the current output mode.
func Mode() string { return mode }

// Emit is the single render path for non-streaming commands.
// In json mode it writes v indented to Stdout, with every json-tagged key
// present (see encodeFilled) and every non-ASCII character escaped (ASCII);
// yaml mode writes the same document as YAML, characters as they are.
// In table mode it calls table with a tabwriter that Emit flushes afterward.
func Emit(v any, table func(w io.Writer)) error {
	if mode == "yaml" {
		data, err := encodeFilled(v)
		if err != nil {
			return err
		}
		y, err := yaml.JSONToYAML(data)
		if err != nil {
			return err
		}
		_, err = Stdout.Write(y)
		return err
	}
	if mode == "json" {
		data, err := encodeFilled(v)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := json.Indent(&buf, ASCII(data), "", "  "); err != nil {
			return err
		}
		_, err = fmt.Fprintln(Stdout, buf.String())
		return err
	}
	tw := newTabWriter(Stdout)
	table(tw)
	return tw.Flush()
}

// Stream renders one streamed record (e.g. a log line) and flushes
// immediately so pipes stream. In json mode it writes v as one compact
// line (NDJSON) with every json-tagged key present, as Emit does; in table
// mode it prints line as-is.
func Stream(v any, line string) error {
	if mode == "yaml" {
		// A YAML stream: one document per record, each opened with ---.
		data, err := encodeFilled(v)
		if err != nil {
			return err
		}
		y, err := yaml.JSONToYAML(data)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(Stdout, "---\n%s", y); err != nil {
			return err
		}
		return flush(Stdout)
	}
	if mode == "json" {
		data, err := encodeFilled(v)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(Stdout, string(ASCII(data))); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(Stdout, line); err != nil {
			return err
		}
	}
	return flush(Stdout)
}

// flush pushes buffered bytes through if the writer supports it.
// os.Stdout is unbuffered in Go, so there is nothing to do for it.
func flush(w io.Writer) error {
	if f, ok := w.(interface{ Flush() error }); ok {
		return f.Flush()
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
	out := make([]byte, 0, len(doc)+16)
	out = append(out, doc[:i]...)
	for i < len(doc) {
		if doc[i] < utf8.RuneSelf {
			out = append(out, doc[i])
			i++
			continue
		}
		r, size := utf8.DecodeRune(doc[i:])
		i += size
		if r > 0xFFFF {
			hi, lo := utf16.EncodeRune(r)
			out = fmt.Appendf(out, `\u%04x\u%04x`, hi, lo)
			continue
		}
		out = fmt.Appendf(out, `\u%04x`, r)
	}
	return out
}
