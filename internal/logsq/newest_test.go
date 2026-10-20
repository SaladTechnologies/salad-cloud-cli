package logsq

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// spacedEntries is total entries 400ms apart from winStart, each received 1.5s after it was emitted, ascending.
func spacedEntries(total int) []Entry {
	all := make([]Entry, 0, total)
	for i := range total {
		emitted := winStart().Add(time.Duration(i) * 400 * time.Millisecond)
		received := emitted.Add(1500 * time.Millisecond)
		all = append(all, ent(
			emitted.Format(time.RFC3339Nano),
			received.Format(time.RFC3339Nano),
			"i",
			emitted.Format("15:04:05.000"),
		))
	}
	return all
}

// runFetchNewest runs FetchNewest over the test window and returns what it emitted and whether it truncated.
func runFetchNewest(t *testing.T, fetch Fetcher, pageSize, limit int, warnLine func(string)) ([]Entry, bool) {
	t.Helper()
	var got []Entry
	truncated, cerr := FetchNewest(
		t.Context(),
		fetch,
		Window{Query: "q", Start: winStart(), End: winEnd(), PageSize: pageSize},
		limit,
		collectEmit(&got),
		warnLine,
	)
	if cerr != nil {
		t.Fatalf("page size %d, limit %d: %v", pageSize, limit, cerr)
	}
	return got, truncated
}

// TestFetchNewestKeepsTheNewestOldestFirst: for every page size and limit, FetchNewest emits the newest limit
// entries in ascending order, each once, and reports truncation exactly when the window holds more.
func TestFetchNewestKeepsTheNewestOldestFirst(t *testing.T) {
	t.Parallel()
	const total = 47
	all := spacedEntries(total)
	for _, size := range []int{1, 3, 10, 46, 47, 100} {
		for _, limit := range []int{1, 5, 46, 47, 48, 100} {
			got, truncated := runFetchNewest(t, realisticFetcher(t, all), size, limit, nil)
			if want := texts(all[max(0, total-limit):]); !slices.Equal(texts(got), want) {
				t.Fatalf("page size %d, limit %d: emitted %v, want %v", size, limit, texts(got), want)
			}
			if wantTrunc := limit < total; truncated != wantTrunc {
				t.Errorf("page size %d, limit %d: truncated = %t, want %t", size, limit, truncated, wantTrunc)
			}
		}
	}
}

// TestFetchNewestSplitMillisecondComesOutOnce: a page that ends inside a millisecond makes the next request end
// just past it, so that millisecond's entries come back; the boundary set drops the ones already kept.
func TestFetchNewestSplitMillisecondComesOutOnce(t *testing.T) {
	t.Parallel()
	all := []Entry{
		ent("2026-03-01T00:00:01.1200000Z", "r", "i", "a"),
		ent("2026-03-01T00:00:01.1230001Z", "r", "i", "b1"),
		ent("2026-03-01T00:00:01.1230002Z", "r", "i", "b2"),
		ent("2026-03-01T00:00:01.1250000Z", "r", "i", "c"),
		ent("2026-03-01T00:00:01.1260000Z", "r", "i", "d"),
	}
	var calls []fetchCall
	record := realisticFetcher(t, all)
	fetch := func(ctx context.Context, req Request) (*Page, *clierr.Error) {
		calls = append(
			calls,
			fetchCall{query: req.Query, start: req.Start, end: req.End, pageSize: req.PageSize, order: req.Order},
		)
		return record(ctx, req)
	}
	var warned []string
	got, truncated := runFetchNewest(t, fetch, 3, 100, func(l string) { warned = append(warned, l) })
	assertTexts(t, got, "a", "b1", "b2", "c", "d")
	if truncated {
		t.Error("truncated = true, want false: the window held 5 entries and the limit was 100")
	}
	if len(warned) != 0 {
		t.Errorf("warned %v, want nothing: no millisecond held more than a page", warned)
	}
	if len(calls) < 2 || calls[1].end != "2026-03-01T00:00:01.124Z" {
		t.Fatalf("calls = %+v, want the second to end just past the split millisecond (01.124)", calls)
	}
}

// TestFetchNewestStopsOnePastTheLimit: the entry past the limit on the same page shows truncation without another
// request; a limit that ends a full page needs one more to tell.
func TestFetchNewestStopsOnePastTheLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		total, limit, size int
		wantCalls          int
		wantTruncated      bool
	}{
		{"inside a page", 47, 5, 10, 1, true},
		{"at a page's end, more left", 47, 10, 10, 2, true},
		{"at a page's end, nothing left", 10, 10, 10, 2, false},
		{"window smaller than the limit", 7, 10, 10, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls []fetchCall
			record := realisticFetcher(t, spacedEntries(tc.total))
			fetch := func(ctx context.Context, req Request) (*Page, *clierr.Error) {
				calls = append(
					calls,
					fetchCall{
						query:    req.Query,
						start:    req.Start,
						end:      req.End,
						pageSize: req.PageSize,
						order:    req.Order,
					},
				)
				return record(ctx, req)
			}
			got, truncated := runFetchNewest(t, fetch, tc.size, tc.limit, nil)
			if len(calls) != tc.wantCalls || truncated != tc.wantTruncated || len(got) != min(tc.total, tc.limit) {
				t.Errorf("calls %d, truncated %t, emitted %d; want %d, %t, %d",
					len(calls), truncated, len(got), tc.wantCalls, tc.wantTruncated, min(tc.total, tc.limit))
			}
		})
	}
}

// TestFetchNewestRequestsDescendingAtMillisecondPrecision: requests read newest first, with the start floored and
// the end ceiled to the millisecond, the only form the API accepts.
func TestFetchNewestRequestsDescendingAtMillisecondPrecision(t *testing.T) {
	t.Parallel()
	var calls []fetchCall
	_, cerr := FetchNewest(
		t.Context(),
		scriptFetcher(nil, &calls),
		Window{
			Query:    "q",
			Start:    winStart().Add(400 * time.Microsecond),
			End:      winEnd().Add(100 * time.Microsecond),
			PageSize: 100,
		},
		10,
		collectEmit(new([]Entry)),
		nil,
	)
	if cerr != nil {
		t.Fatalf("FetchNewest error: %v", cerr)
	}
	want := fetchCall{
		query:    "q",
		start:    "2026-03-01T00:00:00.000Z",
		end:      "2026-03-01T01:00:00.001Z",
		pageSize: 100,
		order:    Descending,
	}
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("calls = %+v, want one %+v", calls, want)
	}
}

// TestFetchNewestStalledMillisecondStepsBack: a full page of repeats that cannot move the end means more than a
// page of entries share its last millisecond; the end steps back 1ms with a warning instead of looping.
func TestFetchNewestStalledMillisecondStepsBack(t *testing.T) {
	t.Parallel()
	x1 := ent("2026-03-01T00:00:01.5000001Z", "r", "i", "x1")
	x2 := ent("2026-03-01T00:00:01.5000002Z", "r", "i", "x2")
	var calls []fetchCall
	fetch := scriptFetcher([]scriptedResult{
		{page: &Page{Items: []Entry{x2, x1}}}, // end moves to 01.501
		{page: &Page{Items: []Entry{x2, x1}}}, // all repeats, end cannot move: step back to 01.500
		{page: &Page{}},
	}, &calls)
	var warned []string
	got, _ := runFetchNewest(t, fetch, 2, 100, func(l string) { warned = append(warned, l) })
	assertTexts(t, got, "x1", "x2")
	if len(warned) != 1 || !strings.Contains(warned[0], "skipping back 1ms") {
		t.Errorf("warned %v, want one skipping-back warning", warned)
	}
	if len(calls) != 3 || calls[2].end != "2026-03-01T00:00:01.500Z" {
		t.Errorf("calls = %+v, want a third ending at 01.500", calls)
	}
}

// TestFetchNewestErrors: a fetch error comes back as is with nothing emitted, and an emit error comes back as a
// clierr.Error.
func TestFetchNewestErrors(t *testing.T) {
	t.Parallel()
	boom := clierr.New(clierr.CodeAuth, clierr.KindAuth, "key revoked")
	var calls []fetchCall
	var got []Entry
	_, cerr := FetchNewest(
		t.Context(),
		scriptFetcher([]scriptedResult{{err: boom}}, &calls),
		Window{Query: "q", Start: winStart(), End: winEnd(), PageSize: 10},
		5,
		collectEmit(&got),
		nil,
	)
	if cerr != boom || len(got) != 0 {
		t.Errorf("error %v with %d emitted, want the fetch error and nothing", cerr, len(got))
	}
	_, cerr = FetchNewest(
		t.Context(),
		realisticFetcher(t, spacedEntries(3)),
		Window{Query: "q", Start: winStart(), End: winEnd(), PageSize: 10},
		5,
		func(Entry) error { return errors.New("broken pipe") },
		nil,
	)
	if cerr == nil || cerr.Kind != clierr.KindAPIError || !strings.Contains(cerr.Message, "broken pipe") {
		t.Errorf("emit error came back as %v, want an api_error naming it", cerr)
	}
}
