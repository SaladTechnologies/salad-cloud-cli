package api

import (
	"slices"
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
	pages := make(map[int][]T)
	get := func(p int) ([]T, *clierr.Error) {
		if v, ok := pages[p]; ok {
			return v, nil
		}
		v, err := fetch(p)
		if err == nil {
			pages[p] = v
		}
		return v, err
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
	s := pageSearch[T]{get: get, pageSize: pageSize, full: 1, empty: 0}
	for p := 2; s.empty == 0; p *= 2 {
		if p > maxProbePage {
			return s.full, nil
		}
		if last, cerr := s.probe(p); cerr != nil || last > 0 {
			return last, cerr
		}
	}
	for s.empty-s.full > 1 {
		if last, cerr := s.probe((s.full + s.empty) / 2); cerr != nil || last > 0 {
			return last, cerr
		}
	}
	return s.full, nil
}

// pageSearch is lastPage's state: the highest page known full and the lowest known empty (0 until one is met).
type pageSearch[T any] struct {
	get         func(int) ([]T, *clierr.Error)
	pageSize    int
	full, empty int
}

// probe reads page p and narrows the search with it: a full page raises full, an empty one lowers empty, and a
// short one is the last page, returned (0 otherwise).
func (s *pageSearch[T]) probe(p int) (int, *clierr.Error) {
	v, cerr := s.get(p)
	switch {
	case cerr != nil:
		return 0, cerr
	case len(v) == s.pageSize:
		s.full = p
	case len(v) > 0:
		return p, nil
	default:
		s.empty = p
	}
	return 0, nil
}

// sortNewestFirst orders items by creation time, newest first; an item whose
// time does not parse sorts last, and equal times keep their order.
func sortNewestFirst[T any](items []T, created func(*T) *string) {
	at := func(item *T) time.Time {
		s := created(item)
		if s == nil {
			return time.Time{}
		}
		t, err := time.Parse(time.RFC3339Nano, *s)
		if err != nil {
			return time.Time{}
		}
		return t
	}
	slices.SortStableFunc(items, func(a, b T) int { return at(&b).Compare(at(&a)) })
}
