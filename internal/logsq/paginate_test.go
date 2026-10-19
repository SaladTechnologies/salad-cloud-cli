package logsq

import (
	"context"
	"testing"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// fetchCall records one Fetcher invocation.
type fetchCall struct {
	query, start, end string
	pageSize          int
}

// scriptedResult is one scripted Fetcher response: a page or an error.
type scriptedResult struct {
	page *Page
	err  *clierr.Error
}

// scriptFetcher returns each scripted result in order; once exhausted it
// returns empty pages. Every call is recorded in *calls.
func scriptFetcher(results []scriptedResult, calls *[]fetchCall) Fetcher {
	i := 0
	return func(_ context.Context, query, start, end string, pageSize int) (*Page, *clierr.Error) {
		*calls = append(*calls, fetchCall{query: query, start: start, end: end, pageSize: pageSize})
		if i >= len(results) {
			return &Page{}, nil
		}
		r := results[i]
		i++
		return r.page, r.err
	}
}

// ent builds a minimal test entry.
func ent(tm, rt, inst, text string) Entry {
	return Entry{Time: tm, ReceiveTime: rt, InstanceID: inst, Text: text}
}

// collectEmit appends emitted entries to *got.
func collectEmit(got *[]Entry) func(Entry) error {
	return func(e Entry) error {
		*got = append(*got, e)
		return nil
	}
}

func texts(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Text)
	}
	return out
}

func assertTexts(t *testing.T, got []Entry, want ...string) {
	t.Helper()
	g := texts(got)
	if len(g) != len(want) {
		t.Fatalf("emitted %d entries %v, want %d %v", len(g), g, len(want), want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("emitted %v, want %v", g, want)
		}
	}
}

var (
	winStart = time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	winEnd   = time.Date(2026, time.March, 1, 1, 0, 0, 0, time.UTC)
)

func TestFetchWindowSinglePartialPage(t *testing.T) {
	var calls []fetchCall
	var got []Entry
	fetch := scriptFetcher([]scriptedResult{
		{page: &Page{
			Items: []Entry{
				ent("2026-03-01T00:00:01Z", "2026-03-01T00:00:02Z", "i1", "a"),
				ent("2026-03-01T00:00:03Z", "2026-03-01T00:00:04Z", "i1", "b"),
			},
			PageMinTime: "2026-03-01T00:00:01Z",
			PageMaxTime: "2026-03-01T00:00:03Z",
		}},
	}, &calls)
	if err := FetchWindow(t.Context(), fetch, "q", winStart, winEnd, 3, collectEmit(&got)); err != nil {
		t.Fatalf("FetchWindow error: %v", err)
	}
	assertTexts(t, got, "a", "b")
	if len(calls) != 1 {
		t.Fatalf("fetch called %d times, want 1", len(calls))
	}
	if calls[0].query != "q" || calls[0].pageSize != 3 {
		t.Errorf("unexpected call: %+v", calls[0])
	}
	// Request times must be the millisecond-precision Z form — the only
	// form the API accepts.
	if calls[0].start != "2026-03-01T00:00:00.000Z" || calls[0].end != "2026-03-01T01:00:00.000Z" {
		t.Errorf("unexpected window: %+v", calls[0])
	}
}

func TestFetchWindowEmptyFirstPage(t *testing.T) {
	var calls []fetchCall
	var got []Entry
	fetch := scriptFetcher(nil, &calls)
	if err := FetchWindow(t.Context(), fetch, "q", winStart, winEnd, 100, collectEmit(&got)); err != nil {
		t.Fatalf("FetchWindow error: %v", err)
	}
	if len(got) != 0 || len(calls) != 1 {
		t.Fatalf("got %d entries in %d calls, want 0 in 1", len(got), len(calls))
	}
}

func TestFetchWindowExactFullPageThenEmpty(t *testing.T) {
	var calls []fetchCall
	var got []Entry
	fetch := scriptFetcher([]scriptedResult{
		{page: &Page{
			Items: []Entry{
				ent("2026-03-01T00:00:01Z", "2026-03-01T00:00:01Z", "i1", "a"),
				ent("2026-03-01T00:00:02Z", "2026-03-01T00:00:02Z", "i1", "b"),
				ent("2026-03-01T00:00:03Z", "2026-03-01T00:00:03Z", "i1", "c"),
			},
			PageMinTime: "2026-03-01T00:00:01Z",
			PageMaxTime: "2026-03-01T00:00:03Z",
		}},
		{page: &Page{}},
	}, &calls)
	if err := FetchWindow(t.Context(), fetch, "q", winStart, winEnd, 3, collectEmit(&got)); err != nil {
		t.Fatalf("FetchWindow error: %v", err)
	}
	assertTexts(t, got, "a", "b", "c")
	if len(calls) != 2 {
		t.Fatalf("fetch called %d times, want 2", len(calls))
	}
	if calls[1].start != "2026-03-01T00:00:03.000Z" {
		t.Errorf("second call start = %s, want the first page's max time floored to ms", calls[1].start)
	}
}

func TestFetchWindowBoundaryDuplicates(t *testing.T) {
	// Entries sitting exactly on PageMaxTime reappear at the start of the
	// next page and must be emitted exactly once.
	c1 := ent("2026-03-01T00:00:03Z", "2026-03-01T00:00:03.5Z", "i1", "c")
	var calls []fetchCall
	var got []Entry
	fetch := scriptFetcher([]scriptedResult{
		{page: &Page{
			Items: []Entry{
				ent("2026-03-01T00:00:01Z", "2026-03-01T00:00:01Z", "i1", "a"),
				ent("2026-03-01T00:00:02Z", "2026-03-01T00:00:02Z", "i1", "b"),
				c1,
			},
			PageMinTime: "2026-03-01T00:00:01Z",
			PageMaxTime: "2026-03-01T00:00:03Z",
		}},
		{page: &Page{
			Items: []Entry{
				c1, // duplicate from the boundary
				ent(
					"2026-03-01T00:00:03Z",
					"2026-03-01T00:00:03.6Z",
					"i2",
					"d",
				), // same time, different entry: NOT a duplicate
				ent("2026-03-01T00:00:04Z", "2026-03-01T00:00:04Z", "i1", "e"),
			},
			PageMinTime: "2026-03-01T00:00:03Z",
			PageMaxTime: "2026-03-01T00:00:04Z",
		}},
	}, &calls)
	if err := FetchWindow(t.Context(), fetch, "q", winStart, winEnd, 3, collectEmit(&got)); err != nil {
		t.Fatalf("FetchWindow error: %v", err)
	}
	assertTexts(t, got, "a", "b", "c", "d", "e")
}

func TestFetchWindowBoundaryMillisecondCarry(t *testing.T) {
	// The API rejects sub-millisecond request times, so the next page's
	// start is PageMaxTime FLOORED to the millisecond. Every entry inside
	// that boundary millisecond — not just the ones exactly at PageMaxTime
	// — can be re-fetched and must be emitted exactly once.
	a := ent("2026-03-01T00:00:03.5550001+00:00", "r1", "i1", "a")
	b := ent("2026-03-01T00:00:03.5553333+00:00", "r2", "i1", "b")
	c := ent("2026-03-01T00:00:03.5559706+00:00", "r3", "i1", "c")
	d := ent("2026-03-01T00:00:03.5559800+00:00", "r4", "i2", "d")
	e := ent("2026-03-01T00:00:03.6000000+00:00", "r5", "i1", "e")
	f := ent("2026-03-01T00:00:03.7000000+00:00", "r6", "i1", "f")
	var calls []fetchCall
	var got []Entry
	fetch := scriptFetcher([]scriptedResult{
		{page: &Page{Items: []Entry{a, b, c}, PageMinTime: a.Time, PageMaxTime: c.Time}},
		// The whole boundary millisecond comes back, plus new entries.
		{page: &Page{Items: []Entry{a, b, c, d, e}, PageMinTime: a.Time, PageMaxTime: e.Time}},
		{page: &Page{Items: []Entry{e, f}, PageMinTime: e.Time, PageMaxTime: f.Time}},
	}, &calls)
	if err := FetchWindow(t.Context(), fetch, "q", winStart, winEnd, 3, collectEmit(&got)); err != nil {
		t.Fatalf("FetchWindow error: %v", err)
	}
	assertTexts(t, got, "a", "b", "c", "d", "e", "f")
	if len(calls) != 3 {
		t.Fatalf("fetch called %d times, want 3", len(calls))
	}
	if want := "2026-03-01T00:00:03.555Z"; calls[1].start != want {
		t.Errorf("second call start = %s, want %s (page_max_time floored to ms)", calls[1].start, want)
	}
	if want := "2026-03-01T00:00:03.600Z"; calls[2].start != want {
		t.Errorf("third call start = %s, want %s", calls[2].start, want)
	}
}

func TestFetchWindowRoundsRequestTimesOutward(t *testing.T) {
	// A sub-millisecond caller window must floor its start and ceil its
	// end, so no boundary entry is excluded by the forced ms precision.
	var calls []fetchCall
	var got []Entry
	fetch := scriptFetcher(nil, &calls)
	start := time.Date(2026, time.March, 1, 0, 0, 0, 400700, time.UTC) // .0004007s
	end := time.Date(2026, time.March, 1, 0, 0, 1, 2000500, time.UTC)  // 1.0020005s
	if err := FetchWindow(t.Context(), fetch, "q", start, end, 100, collectEmit(&got)); err != nil {
		t.Fatalf("FetchWindow error: %v", err)
	}
	if want := "2026-03-01T00:00:00.000Z"; calls[0].start != want {
		t.Errorf("start = %s, want %s (floored)", calls[0].start, want)
	}
	if want := "2026-03-01T00:00:01.003Z"; calls[0].end != want {
		t.Errorf("end = %s, want %s (ceiled)", calls[0].end, want)
	}
}

func TestFetchWindowSameTimestampLoopGuard(t *testing.T) {
	// More entries share one timestamp than fit in a page: the boundary
	// cannot advance, so after one stuck re-fetch the window skips 1ms
	// instead of looping forever.
	ts := "2026-03-01T00:00:01Z"
	a := ent(ts, ts, "i1", "a")
	b := ent(ts, ts, "i2", "b")
	stuck := &Page{Items: []Entry{a, b}, PageMinTime: ts, PageMaxTime: ts}
	var calls []fetchCall
	var got []Entry
	fetch := scriptFetcher([]scriptedResult{
		{page: stuck},
		{page: stuck},
		{page: &Page{}},
	}, &calls)
	err := FetchWindow(t.Context(), fetch, "q",
		time.Date(2026, time.March, 1, 0, 0, 1, 0, time.UTC), winEnd, 2, collectEmit(&got))
	if err != nil {
		t.Fatalf("FetchWindow error: %v", err)
	}
	assertTexts(t, got, "a", "b")
	if len(calls) != 3 {
		t.Fatalf("fetch called %d times, want 3", len(calls))
	}
	if wantStart := "2026-03-01T00:00:01.001Z"; calls[2].start != wantStart {
		t.Errorf("third call start = %s, want %s (1ms past the stuck millisecond)", calls[2].start, wantStart)
	}
}

func TestFetchWindowEmitErrorPropagates(t *testing.T) {
	var calls []fetchCall
	fetch := scriptFetcher([]scriptedResult{
		{page: &Page{
			Items:       []Entry{ent("2026-03-01T00:00:01Z", "2026-03-01T00:00:01Z", "i1", "a")},
			PageMinTime: "2026-03-01T00:00:01Z",
			PageMaxTime: "2026-03-01T00:00:01Z",
		}},
	}, &calls)
	want := clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "pipe closed")
	err := FetchWindow(t.Context(), fetch, "q", winStart, winEnd, 100, func(Entry) error {
		return want
	})
	if err != want {
		t.Fatalf("FetchWindow error = %v, want the emit error", err)
	}
}

func TestFetchWindowFetchErrorPropagates(t *testing.T) {
	var calls []fetchCall
	want := clierr.New(clierr.CodeRateLimited, clierr.KindRateLimited, "slow down")
	fetch := scriptFetcher([]scriptedResult{{err: want}}, &calls)
	err := FetchWindow(t.Context(), fetch, "q", winStart, winEnd, 100, func(Entry) error { return nil })
	if err != want {
		t.Fatalf("FetchWindow error = %v, want the fetch error", err)
	}
}

func TestDedupKeyDistinguishesEntries(t *testing.T) {
	base := Entry{Time: "t", ReceiveTime: "r", InstanceID: "i", Text: "x"}
	variants := []Entry{
		{Time: "t2", ReceiveTime: "r", InstanceID: "i", Text: "x"},
		{Time: "t", ReceiveTime: "r2", InstanceID: "i", Text: "x"},
		{Time: "t", ReceiveTime: "r", InstanceID: "i2", Text: "x"},
		{Time: "t", ReceiveTime: "r", InstanceID: "i", Text: "y"},
		{Time: "t", ReceiveTime: "r", InstanceID: "i", Text: "x", JSON: map[string]any{"k": "v"}},
	}
	for i, v := range variants {
		if DedupKey(base) == DedupKey(v) {
			t.Errorf("variant %d collides with base: %+v", i, v)
		}
	}
	if first := DedupKey(base); first != DedupKey(base) { // two calls on purpose: the key must not vary between calls
		t.Error("DedupKey is not stable")
	}
	j1 := Entry{JSON: map[string]any{"a": 1, "b": 2}}
	j2 := Entry{JSON: map[string]any{"b": 2, "a": 1}}
	if DedupKey(j1) != DedupKey(j2) {
		t.Error("JSON canonicalization should be key-order independent")
	}
}

func TestDedupKeyUsesRawTimestampStrings(t *testing.T) {
	// The same instant in two spellings must produce two different keys —
	// keys must never parse-and-reformat timestamps.
	a := Entry{Time: "2026-03-01T00:00:01.1200000Z", ReceiveTime: "r", InstanceID: "i", Text: "x"}
	b := Entry{Time: "2026-03-01T00:00:01.12Z", ReceiveTime: "r", InstanceID: "i", Text: "x"}
	if DedupKey(a) == DedupKey(b) {
		t.Error("DedupKey appears to normalize timestamps")
	}
}

// realisticFetcher serves entries the way the live API does: the earliest
// pageSize entries whose emission Time is at or after start, ascending,
// with PageMinTime/PageMaxTime set to the first and last entries'
// RECEIVE time, which runs behind emission.
func realisticFetcher(t *testing.T, all []Entry) Fetcher {
	t.Helper()
	return func(_ context.Context, _, start, _ string, pageSize int) (*Page, *clierr.Error) {
		from, err := time.Parse(time.RFC3339Nano, start)
		if err != nil {
			panic(err)
		}
		page := &Page{}
		for _, e := range all {
			emitted, err := time.Parse(time.RFC3339Nano, e.Time)
			if err != nil {
				t.Error(err)
				continue
			}
			if emitted.Before(from) {
				continue
			}
			page.Items = append(page.Items, e)
			if len(page.Items) == pageSize {
				break
			}
		}
		if len(page.Items) > 0 {
			page.PageMinTime = page.Items[0].ReceiveTime
			page.PageMaxTime = page.Items[len(page.Items)-1].ReceiveTime
		}
		return page, nil
	}
}

// TestFetchWindowResumesFromEmissionTimeNotPageMaxTime: page_max_time is a
// receive time, later than the page's newest emission time. Paging from it
// skipped the entries emitted in between, so the total depended on the page
// size. Every page size must now return every entry exactly once, in order.
func TestFetchWindowResumesFromEmissionTimeNotPageMaxTime(t *testing.T) {
	const total = 47
	all := make([]Entry, 0, total)
	for i := range total {
		emitted := winStart.Add(time.Duration(i) * 400 * time.Millisecond)
		received := emitted.Add(1500 * time.Millisecond)
		all = append(
			all,
			ent(
				emitted.Format(time.RFC3339Nano),
				received.Format(time.RFC3339Nano),
				"i",
				emitted.Format("15:04:05.000"),
			),
		)
	}
	for _, size := range []int{1, 3, 10, 46, 47, 100} {
		var got []Entry
		cerr := FetchWindow(
			t.Context(),
			realisticFetcher(t, all),
			"q",
			winStart,
			winEnd,
			size,
			collectEmit(&got),
		)
		if cerr != nil {
			t.Fatalf("page size %d: %v", size, cerr)
		}
		if len(got) != len(all) {
			t.Fatalf("page size %d: emitted %d of %d entries", size, len(got), len(all))
		}
		for i := range all {
			if got[i].Text != all[i].Text {
				t.Fatalf("page size %d: entry %d is %s, want %s", size, i, got[i].Text, all[i].Text)
			}
		}
	}
}
