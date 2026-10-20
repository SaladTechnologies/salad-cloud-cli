package logsq

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// FetchNewest emits the newest limit entries in [w.Start, w.End), oldest first, and reports whether the window holds
// older entries it left out. It reads the window newest first (the API's descending order) and stops one entry past
// limit, so a limit costs about limit/PageSize requests however wide the window is; the entries it keeps are held
// until the read ends, since the oldest of them comes last.
//
// Paging is FetchWindow's mirrored: each next request ends just past the millisecond of the page's oldest emission
// time (end times are exclusive and millisecond-precision), so that millisecond is fetched again and a boundary set
// of the DedupKeys already kept there suppresses the repeats.
func FetchNewest(
	ctx context.Context,
	fetch Fetcher,
	w Window,
	limit int,
	emit func(Entry) error,
	warnLine func(string),
) (bool, *clierr.Error) {
	entries, truncated, cerr := readNewest(ctx, fetch, w, limit, warnLine)
	if cerr != nil {
		return false, cerr
	}
	for i := range entries {
		if err := emit(entries[i]); err != nil {
			return false, asCliErr(err)
		}
	}
	return truncated, nil
}

// readNewest is FetchNewest's read: the newest limit entries in w, oldest first, and whether older ones were left
// out.
func readNewest(
	ctx context.Context,
	fetch Fetcher,
	w Window,
	limit int,
	warnLine func(string),
) ([]Entry, bool, *clierr.Error) {
	r := newestReader{limit: limit, newest: nil, boundary: make(map[string]time.Time), truncated: false}
	startT := w.Start.UTC().Truncate(time.Millisecond)
	start := FormatRequestTime(startT)
	end := ceilMillisecond(w.End)
	for end.After(startT) {
		page, cerr := fetch(
			ctx,
			Request{Query: w.Query, Start: start, End: FormatRequestTime(end), PageSize: w.PageSize, Order: Descending},
		)
		if cerr != nil {
			return nil, false, cerr
		}
		dups := r.keep(page.Items)
		if r.truncated || len(page.Items) < w.PageSize {
			break // the limit is reached, or a partial page: the window is drained
		}
		oldest, ok := oldestEntryTime(page.Items)
		if !ok {
			return nil, false, clierr.New(clierr.CodeGeneric, clierr.KindAPIError,
				"could not parse the time of any log entry on a full page returned by the API")
		}
		next := oldest.Truncate(time.Millisecond).Add(time.Millisecond)
		newBoundary := carryBoundary(page.Items, r.boundary, func(t time.Time) bool { return t.Before(next) })
		if !next.Before(end) {
			next = end // re-fetch the same end; newBoundary now covers what was kept
			if dups == len(page.Items) {
				next = skipStalledMillisecondBack(end, newBoundary, w.PageSize, warnLine)
			}
		}
		r.boundary, end = newBoundary, next
	}
	slices.Reverse(r.newest)
	return r.newest, r.truncated, nil
}

// newestReader is one FetchNewest's state: the entries kept so far, newest first, and the boundary set of the ones
// the next request can return again.
type newestReader struct {
	limit     int
	newest    []Entry
	boundary  map[string]time.Time
	truncated bool
}

// keep appends a page's entries that the boundary does not hold, until one past the limit shows the window holds
// more than it keeps, and returns how many it skipped as repeats.
func (r *newestReader) keep(items []Entry) int {
	dups := 0
	for i := range items {
		if _, seen := r.boundary[DedupKey(&items[i])]; seen {
			dups++
			continue
		}
		if len(r.newest) == r.limit {
			r.truncated = true
			return dups
		}
		r.newest = append(r.newest, items[i])
	}
	return dups
}

// skipStalledMillisecondBack is skipStalledMillisecond for a window read newest first: more than pageSize entries
// share the window's last millisecond, so the end steps back 1ms, says so, and drops the boundary entries the new
// end excludes.
func skipStalledMillisecondBack(
	end time.Time,
	boundary map[string]time.Time,
	pageSize int,
	warnLine func(string),
) time.Time {
	next := end.Add(-time.Millisecond)
	warn(warnLine, fmt.Sprintf(
		"warning: more than %d log entries share the millisecond at %s; "+
			"skipping back 1ms (some entries in that millisecond may be missed)",
		pageSize,
		FormatRequestTime(next),
	))
	for k, t := range boundary {
		if !t.Before(next) {
			delete(boundary, k)
		}
	}
	return next
}
