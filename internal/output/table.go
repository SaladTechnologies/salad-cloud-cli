package output

import (
	"fmt"
	"io"
	"text/tabwriter"
)

// newTabWriter returns the tabwriter used by Emit's table mode:
// minwidth 0, tabwidth 8, padding 2, pad char ' '.
func newTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
}

// Writer is what Emit hands a table function. Print, Printf and Println write
// to the table and keep the first write error, and Emit returns that error
// once the table is written, so a table function needs no error handling of
// its own. After an error, later writes are skipped.
type Writer struct {
	w   io.Writer
	err error
}

// NewWriter returns a Writer over w, for rendering a table function outside
// Emit (tests).
func NewWriter(w io.Writer) *Writer { return &Writer{w: w, err: nil} }

// Print writes its operands as [fmt.Fprint] does.
func (w *Writer) Print(a ...any) {
	if w.err == nil {
		_, w.err = fmt.Fprint(w.w, a...)
	}
}

// Printf writes its operands as [fmt.Fprintf] does.
func (w *Writer) Printf(format string, a ...any) {
	if w.err == nil {
		_, w.err = fmt.Fprintf(w.w, format, a...)
	}
}

// Println writes its operands as [fmt.Fprintln] does.
func (w *Writer) Println(a ...any) {
	if w.err == nil {
		_, w.err = fmt.Fprintln(w.w, a...)
	}
}

// Write implements [io.Writer], keeping the first error as the other methods
// do.
func (w *Writer) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	var n int
	n, w.err = w.w.Write(p)
	return n, w.err
}

// Err returns the first write error, or nil.
func (w *Writer) Err() error { return w.err }
