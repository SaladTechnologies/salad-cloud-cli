package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// pagedServer serves total items in pages of the requested size with
// total_size null, the way a listing can arrive; key is the item's
// identifying field. It counts the pages asked for.
func pagedServer(t *testing.T, total int, key string, requests *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(requests, 1)
		page := should(strconv.Atoi(r.URL.Query().Get("page")))(t)
		size := should(strconv.Atoi(r.URL.Query().Get("page_size")))(t)
		var items []string
		for i := (page - 1) * size; i < page*size && i < total; i++ {
			items = append(items, fmt.Sprintf(`{%q:"item-%d"}`, key, i))
		}
		respond(t, w, fmt.Sprintf(
			`{"items":[%s],"page":%d,"page_size":%d,"total_size":null}`,
			strings.Join(items, ","),
			page,
			size,
		))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A null total_size never ends or breaks a listing: the short page does.
func TestListInferenceEndpointsPagesWithNullTotal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ total, pages int }{{60, 3}, {50, 3}, {3, 1}, {0, 1}} {
		var n int32
		c := newRestTestClient(t, pagedServer(t, tc.total, "name", &n).URL)
		coll, cerr := c.ListInferenceEndpoints(t.Context())
		if cerr != nil {
			t.Fatal(cerr)
		}
		if len(coll.Items) != tc.total || int(n) != tc.pages {
			t.Errorf(
				"%d endpoints: listed %d in %d requests, want %d in %d",
				tc.total,
				len(coll.Items),
				n,
				tc.total,
				tc.pages,
			)
		}
	}
}

// jobListing is the order a job listing answers in.
type jobListing int

// The two job listings' orders.
const (
	inferenceListing jobListing = iota // oldest first
	queueListing                       // job ID order
)

// jobServer serves total jobs in pages, total_size null and a page past the
// end empty, the way both job listings answer. Job i was created i seconds
// after a base time. The queue listing lists them in job ID order (IDs scrambled against creation); the inference
// listing oldest first.
func jobServer(t *testing.T, total int, listing jobListing, requests *int32) *httptest.Server {
	t.Helper()
	base := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	order := make([]int, total)
	for i := range order {
		order[i] = i
	}
	id := func(i int) string { return fmt.Sprintf("%08d", (i*7919)%10007) }
	if listing == queueListing {
		slices.SortFunc(order, func(a, b int) int { return strings.Compare(id(a), id(b)) })
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(requests, 1)
		page := should(strconv.Atoi(r.URL.Query().Get("page")))(t)
		size := should(strconv.Atoi(r.URL.Query().Get("page_size")))(t)
		var items []string
		for k := (page - 1) * size; k < page*size && k < total; k++ {
			i := order[k]
			items = append(items, fmt.Sprintf(`{"id":%q,"create_time":%q,"input":{"n":%d}}`,
				id(i), base.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano), i))
		}
		respond(t, w, fmt.Sprintf(
			`{"items":[%s],"page":%d,"page_size":%d,"total_size":null}`,
			strings.Join(items, ","),
			page,
			size,
		))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newestN is the creation indexes the newest want jobs of total have.
func newestN(total, want int) []int {
	var out []int
	for i := total - 1; i >= 0 && len(out) < want; i-- {
		out = append(out, i)
	}
	return out
}

// Inference jobs come oldest first; the listing reads from the end, newest
// first, and a long listing costs a search, not every page.
func TestListInferenceJobsNewestFirst(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		total, limit, want, maxRequests int
		more                            bool
	}{
		{60, 0, 60, 4, false}, // pages 1, 2 full, 4 empty, 3 short
		{60, 30, 30, 4, true}, // the newest 30: pages 3 and 2
		{60, 25, 25, 4, true},
		{50, 0, 50, 4, false}, // two full pages, then empty ones
		{50, 50, 50, 4, false},
		{10, 100, 10, 1, false}, // one short page
		{0, 100, 0, 1, false},
		{1000, 5, 5, 15, true}, // 40 pages: a search, not 40 requests
	} {
		var n int32
		c := newRestTestClient(t, jobServer(t, tc.total, inferenceListing, &n).URL)
		jobs, more, cerr := c.ListInferenceJobs(t.Context(), "transcribe", tc.limit)
		if cerr != nil {
			t.Fatal(cerr)
		}
		got := make([]int, len(jobs))
		for i, j := range jobs {
			got[i] = int(assertAs[float64](t, assertAs[map[string]any](t, j.Input)["n"]))
		}
		if fmt.Sprint(got) != fmt.Sprint(newestN(tc.total, tc.want)) || more != tc.more || int(n) > tc.maxRequests {
			t.Errorf("%d jobs, limit %d: got %v (more %v) in %d requests, want the newest %d (more %v) in at most %d",
				tc.total, tc.limit, got, more, n, tc.want, tc.more, tc.maxRequests)
		}
	}
}

// Queue jobs come in job ID order; the listing reads every page and sorts
// by creation, newest first.
func TestListQueueJobsNewestFirst(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		total, limit, want, requests int
		more                         bool
	}{
		{60, 0, 60, 3, false},
		{60, 30, 30, 3, true},
		{60, 60, 60, 3, false},
		{50, 50, 50, 3, false}, // the empty third page says there is no more
		{10, 100, 10, 1, false},
	} {
		var n int32
		c := newRestTestClient(t, jobServer(t, tc.total, queueListing, &n).URL)
		listing, cerr := c.ListQueueJobs(t.Context(), "q", tc.limit)
		if cerr != nil {
			t.Fatal(cerr)
		}
		jobs, more := listing.Jobs, listing.More
		got := make([]int, len(jobs))
		for i, j := range jobs {
			got[i] = int(assertAs[float64](t, assertAs[map[string]any](t, j.Input)["n"]))
		}
		if fmt.Sprint(got) != fmt.Sprint(newestN(tc.total, tc.want)) || more != tc.more || int(n) != tc.requests {
			t.Errorf("%d jobs, limit %d: got %v (more %v) in %d requests, want the newest %d (more %v) in %d",
				tc.total, tc.limit, got, more, n, tc.want, tc.more, tc.requests)
		}
	}
}

func TestJobsWanted(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ limit, want int }{{0, 10000}, {100, 100}, {20000, 10000}, {10000, 10000}} {
		if got := jobsWanted(tc.limit, 10000); got != tc.want {
			t.Errorf("jobsWanted(%d) = %d, want %d", tc.limit, got, tc.want)
		}
	}
}

// TestListQueueJobsReadBudget: with a limit, the listing stops after
// QueueJobsBudget jobs and says the result is partial; --limit 0 reads
// every page.
func TestListQueueJobsReadBudget(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		total, limit, requests int
		partial                bool
	}{
		{2000, 100, 40, true},  // the budget: 40 pages, more left unread
		{1000, 100, 40, true},  // exactly the budget: a full 40th page may not be the last
		{2000, 1500, 60, true}, // a limit above the budget reads its own worth
		{2000, 0, 81, false},   // 0 reads everything
		{120, 100, 5, false},   // a short queue is read to the end
	} {
		var n int32
		c := newRestTestClient(t, jobServer(t, tc.total, queueListing, &n).URL)
		listing, cerr := c.ListQueueJobs(t.Context(), "q", tc.limit)
		if cerr != nil {
			t.Fatal(cerr)
		}
		jobs, partial := listing.Jobs, listing.Partial
		if int(n) != tc.requests || partial != tc.partial {
			t.Errorf(
				"%d jobs, limit %d: %d requests (partial %v), want %d (partial %v)",
				tc.total,
				tc.limit,
				n,
				partial,
				tc.requests,
				tc.partial,
			)
		}
		if want := min(tc.total, max(tc.limit, 0)); tc.limit > 0 && len(jobs) != want {
			t.Errorf("%d jobs, limit %d: listed %d, want %d", tc.total, tc.limit, len(jobs), want)
		}
	}
}
