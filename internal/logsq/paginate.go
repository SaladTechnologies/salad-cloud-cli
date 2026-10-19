package logsq

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// Entry is logsq's own flat view of one log entry, so this package stays
// SDK-free and easily testable. The cli layer adapts SDK entries into it,
// keeping Raw as the original SDK value for full-fidelity NDJSON output.
type Entry struct {
	Time        string // node emission time, raw RFC3339 string as returned
	ReceiveTime string // ingestion time, raw RFC3339 string as returned
	Severity    string
	Project     string // resource.labels.project_name
	Group       string // resource.labels.container_group_name
	InstanceID  string
	MachineID   string
	Text        string
	JSON        any
	Raw         any
}

// Page is one page of query results.
type Page struct {
	Items                    []Entry
	PageMinTime, PageMaxTime string
}

// Fetcher abstracts the API: it returns one page of entries for
// [start, end) matching the compiled query, in ascending order.
type Fetcher func(ctx context.Context, query, startRFC3339, endRFC3339 string, pageSize int) (*Page, *clierr.Error)

// DedupKey returns a stable identity for e. Log entries have no unique ID,
// so pagination and follow dedup hash the fields that together identify an
// entry. RAW STRINGS ONLY: timestamps are never parsed and reformatted for
// the key, because a format round-trip can lose sub-second precision.
func DedupKey(e Entry) string {
	jsonPart := ""
	if e.JSON != nil {
		if b, err := json.Marshal(e.JSON); err == nil {
			jsonPart = string(b) // json.Marshal sorts map keys: canonical enough
		} else {
			jsonPart = fmt.Sprintf("%v", e.JSON)
		}
	}
	h := sha1.Sum([]byte(e.Time + "\x00" + e.ReceiveTime + "\x00" + e.InstanceID + "\x00" + e.Text + jsonPart))
	return hex.EncodeToString(h[:])
}

// FetchWindow streams every entry in [start, end) to emit, in ascending
// order, following full pages from the newest emission time on each page.
// Request times are rendered at the API's mandatory millisecond precision:
// the start floored, the end ceiled (see times.go). Because the next page's
// start is the FLOOR of that time (the raw 7-digit value would be rejected
// with invalid_time_range), up to one millisecond of already-emitted entries
// can be re-fetched at each page boundary; a boundary set holding the
// DedupKeys (and parsed times) of every already-emitted entry at or after
// that floored millisecond suppresses the repeats. Entries have no unique
// IDs, hence the keys.
//
// The API's page_max_time is NOT the resume point: it is the newest
// receive_time (ingestion) on the page, which runs a second or more behind
// emission. Resuming from it skipped every entry emitted between a page's
// last entry and that entry's ingestion.
func FetchWindow(
	ctx context.Context,
	fetch Fetcher,
	query string,
	start, end time.Time,
	pageSize int,
	emit func(Entry) error,
	warnLine func(string),
) *clierr.Error {
	cur := start.UTC().Truncate(time.Millisecond)
	endStr := CeilRequestTime(end)
	boundary := make(map[string]time.Time) // already-emitted entries with parsed Time >= cur
	for {
		page, cerr := fetch(ctx, query, FormatRequestTime(cur), endStr, pageSize)
		if cerr != nil {
			return cerr
		}
		dups, cerr := emitNew(page.Items, boundary, emit)
		if cerr != nil {
			return cerr
		}
		if len(page.Items) < pageSize {
			return nil // partial page: the window is drained
		}
		maxT, ok := newestEntryTime(page.Items)
		if !ok {
			return clierr.New(clierr.CodeGeneric, clierr.KindAPIError,
				"could not parse the time of any log entry on a full page returned by the API")
		}
		next := maxT.Truncate(time.Millisecond)
		newBoundary := carryBoundary(page.Items, boundary, next)
		if !next.After(cur) {
			next = cur // re-fetch the same start; newBoundary now covers what was emitted
			if dups == len(page.Items) {
				next = skipStalledMillisecond(cur, newBoundary, pageSize, warnLine)
			}
		}
		boundary, cur = newBoundary, next
	}
}

// emitNew emits a page's entries in order, skipping the ones boundary holds (emitted already, from the millisecond
// the request started at), and returns how many it skipped.
func emitNew(items []Entry, boundary map[string]time.Time, emit func(Entry) error) (int, *clierr.Error) {
	dups := 0
	for i := range items {
		e := items[i]
		if _, seen := boundary[DedupKey(e)]; seen {
			dups++
			continue
		}
		if err := emit(e); err != nil {
			return dups, asCliErr(err)
		}
	}
	return dups, nil
}

// carryBoundary is the boundary set for a request starting at next: every already-emitted entry it can re-return,
// which is this page's entries at or after next plus the previous carries still at or after next (relevant when the
// window fails to advance).
func carryBoundary(items []Entry, boundary map[string]time.Time, next time.Time) map[string]time.Time {
	newBoundary := make(map[string]time.Time)
	for i := range items {
		t, perr := time.Parse(time.RFC3339Nano, items[i].Time)
		if perr != nil {
			continue // unparseable Time cannot be placed on the boundary
		}
		if t = t.UTC(); !t.Before(next) {
			newBoundary[DedupKey(items[i])] = t
		}
	}
	for k, t := range boundary {
		if !t.Before(next) {
			newBoundary[k] = t
		}
	}
	return newBoundary
}

// skipStalledMillisecond is the next request's start when a full page held nothing new and did not move the window
// past cur. Pathological: more than pageSize entries share one millisecond, so the boundary cannot advance. Rather
// than loop forever it skips 1ms, says so, and drops the boundary entries before the new start.
func skipStalledMillisecond(
	cur time.Time,
	boundary map[string]time.Time,
	pageSize int,
	warnLine func(string),
) time.Time {
	warn(warnLine, fmt.Sprintf(
		"warning: more than %d log entries share the millisecond at %s; skipping ahead 1ms (some entries in that millisecond may be missed)",
		pageSize,
		FormatRequestTime(cur),
	))
	next := cur.Add(time.Millisecond)
	for k, t := range boundary {
		if t.Before(next) {
			delete(boundary, k)
		}
	}
	return next
}

// newestEntryTime returns the latest parseable emission Time among items,
// in UTC; ok is false when none parses.
func newestEntryTime(items []Entry) (newest time.Time, ok bool) {
	for i := range items {
		t, err := time.Parse(time.RFC3339Nano, items[i].Time)
		if err != nil {
			continue
		}
		if t = t.UTC(); !ok || t.After(newest) {
			newest, ok = t, true
		}
	}
	return newest, ok
}

func asCliErr(err error) *clierr.Error {
	var ce *clierr.Error
	if errors.As(err, &ce) {
		return ce
	}
	return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "%v", err)
}

// warn hands line to to, a warning: line for stderr, when there is a to.
func warn(to func(string), line string) {
	if to != nil {
		to(line)
	}
}
