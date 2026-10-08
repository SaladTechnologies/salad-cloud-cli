package output

import (
	"io"
	"text/tabwriter"
)

// newTabWriter returns the tabwriter used by Emit's table mode:
// minwidth 0, tabwidth 8, padding 2, pad char ' '.
func newTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
}
