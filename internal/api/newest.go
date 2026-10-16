package api

import (
	"slices"
	"sort"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// Job listings are shown newest first, which neither API offers: neither
// takes a sort order, inference endpoint jobs come oldest first, and queue
// jobs come in job ID order. Their totals are null, so
// a listing cannot jump to its last page either.

// maxProbePage bounds the search for an oldest-first listing's last page.
const maxProbePage = 1 << 20

// newestPages reads an oldest-first paged listing from its end: it finds the
// last page by doubling and then bisecting the page number (a page past the
// end comes back empty), then reads backwards until want items are in hand,
// so a short listing costs what it did and a long one a few dozen requests
// instead of every page. Items come back newest first; more reports that
// older items were left out.
func newestPages[T any](
	fetch func(page int) ([]T, *clierr.Error),
	pageSize, want int,
) (items []T, more bool, cerr *clierr.Error) {
	pages := map[int][]T{}
	get := func(p int) ([]T, *clierr.Error) {
		if v, ok := pages[p]; ok {
			return v, nil
		}
		v, cerr := fetch(p)
		if cerr == nil {
			pages[p] = v
		}
		return v, cerr
	}
	last, cerr := lastPage(get, pageSize)
	if cerr != nil {
		return nil, false, cerr
	}
	p := last
	for ; p >= 1 && len(items) < want; p-- {
		v, cerr := get(p)
		if cerr != nil {
			return nil, false, cerr
		}
		for _, item := range slices.Backward(v) {
			items = append(items, item)
		}
	}
	more = p >= 1 || len(items) > want
	if len(items) > want {
		items = items[:want]
	}
	return items, more, nil
}

// lastPage finds the last non-empty page: 1 when the first page is short,
// else the first short page met, or the last full one before an empty page.
func lastPage[T any](get func(int) ([]T, *clierr.Error), pageSize int) (int, *clierr.Error) {
	first, cerr := get(1)
	if cerr != nil {
		return 0, cerr
	}
	if len(first) < pageSize {
		return 1, nil
	}
	full, empty := 1, 0 // the highest page known full, the lowest known empty
	for p := 2; empty == 0; p *= 2 {
		if p > maxProbePage {
			return full, nil
		}
		v, cerr := get(p)
		switch {
		case cerr != nil:
			return 0, cerr
		case len(v) == pageSize:
			full = p
		case len(v) > 0:
			return p, nil
		default:
			empty = p
		}
	}
	for empty-full > 1 {
		mid := (full + empty) / 2
		v, cerr := get(mid)
		switch {
		case cerr != nil:
			return 0, cerr
		case len(v) == pageSize:
			full = mid
		case len(v) > 0:
			return mid, nil
		default:
			empty = mid
		}
	}
	return full, nil
}

// sortNewestFirst orders items by creation time, newest first; an item whose
// time does not parse sorts last, and equal times keep their order.
func sortNewestFirst[T any](items []T, created func(*T) *string) {
	at := func(i int) time.Time {
		s := created(&items[i])
		if s == nil {
			return time.Time{}
		}
		t, err := time.Parse(time.RFC3339Nano, *s)
		if err != nil {
			return time.Time{}
		}
		return t
	}
	sort.SliceStable(items, func(i, j int) bool { return at(i).After(at(j)) })
}
