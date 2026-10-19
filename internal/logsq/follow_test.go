package logsq

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// fakeClock advances a scripted clock. It allows a fixed number of sleeps
// to proceed; the next sleep cancels the context, ending the follow loop
// the way a SIGINT would.
type fakeClock struct {
	now     time.Time
	sleeps  []time.Duration
	allowed int
	cancel  context.CancelFunc
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.sleeps = append(c.sleeps, d)
	if len(c.sleeps) > c.allowed {
		c.cancel()
		return ctx.Err()
	}
	c.now = c.now.Add(d)
	return nil
}

// followNow is the fake clock's start in the follow tests.
func followNow() time.Time { return time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC) }

// followSince is where the follow tests' backlog starts.
func followSince() time.Time { return followNow().Add(-10 * time.Minute) }

// staticQuery is the single compiled query the follow loop uses for the
// backlog and every poll — there is no per-poll recompilation any more.
func staticQuery(t *testing.T) string {
	t.Helper()
	q, err := Compile(Filters{Group: "g"})
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	return q
}

// runFollower wires a Follower with fakes and runs it to completion,
// returning the follower itself so tests can inspect its seen map.
func runFollower(
	t *testing.T,
	results []scriptedResult,
	allowedSleeps int,
	poll, maxBackoff, lookback time.Duration,
) (emitted []Entry, calls []fetchCall, sleeps []time.Duration, f *Follower) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	clock := &fakeClock{now: followNow(), allowed: allowedSleeps, cancel: cancel}
	fetch := scriptFetcher(results, &calls)
	f = &Follower{Poll: poll, MaxBackoff: maxBackoff, Lookback: lookback, Clock: clock}
	if err := f.Run(ctx, staticQuery(t), fetch, followSince(), 100, collectEmit(&emitted)); err != nil {
		t.Fatalf("Run returned %v, want nil on cancellation", err)
	}
	return emitted, calls, clock.sleeps, f
}

func page(items ...Entry) scriptedResult {
	p := &Page{Items: items}
	if len(items) > 0 {
		p.PageMinTime = items[0].Time
		p.PageMaxTime = items[len(items)-1].Time
	}
	return scriptedResult{page: p}
}

func TestFollowBacklogThenPolls(t *testing.T) {
	t.Parallel()
	e1 := ent("2026-03-01T09:51:00Z", "2026-03-01T09:51:30Z", "i1", "e1")
	e2 := ent("2026-03-01T09:55:00Z", "2026-03-01T09:55:30Z", "i1", "e2")
	e3 := ent("2026-03-01T09:59:00Z", "2026-03-01T10:00:02Z", "i1", "e3")
	e4 := ent("2026-03-01T10:00:06Z", "2026-03-01T10:00:07Z", "i1", "e4")
	emitted, calls, _, _ := runFollower(t, []scriptedResult{
		page(e1, e2), // backlog
		page(e3),     // poll 1
		page(e4),     // poll 2
	}, 2, 5*time.Second, 0, 0)
	assertTexts(t, emitted, "e1", "e2", "e3", "e4")
	if len(calls) != 3 {
		t.Fatalf("fetch called %d times, want 3", len(calls))
	}
	// One static query throughout: no receive_time (not queryable), no
	// per-poll recompilation.
	for i, c := range calls {
		if strings.Contains(c.query, "receive_time") {
			t.Errorf("call %d query references receive_time (not a queryable field): %s", i, c.query)
		}
		if c.query != calls[0].query {
			t.Errorf("call %d query differs from the backlog query: %s vs %s", i, c.query, calls[0].query)
		}
	}
	// Backlog windows span [since, now+lookahead), ms-formatted.
	if want := "2026-03-01T09:50:00.000Z"; calls[0].start != want {
		t.Errorf("backlog window start = %s, want %s", calls[0].start, want)
	}
	if want := "2026-03-01T10:01:00.000Z"; calls[0].end != want {
		t.Errorf("backlog window end = %s, want %s", calls[0].end, want)
	}
	// Poll windows slide: [maxSeenTime - Lookback (2m default), now+1m).
	if want := "2026-03-01T09:53:00.000Z"; calls[1].start != want {
		t.Errorf("poll 1 window start = %s, want %s (max seen 09:55 - 2m)", calls[1].start, want)
	}
	if want := "2026-03-01T10:01:05.000Z"; calls[1].end != want {
		t.Errorf("poll 1 window end = %s, want %s", calls[1].end, want)
	}
	if want := "2026-03-01T09:57:00.000Z"; calls[2].start != want {
		t.Errorf("poll 2 window start = %s, want %s (max seen 09:59 - 2m)", calls[2].start, want)
	}
}

func TestFollowLookbackRescanSkipsSeen(t *testing.T) {
	t.Parallel()
	// Every poll re-scans the lookback window, so already-emitted entries
	// keep coming back; the seen set must swallow them.
	e1 := ent("2026-03-01T09:59:00Z", "2026-03-01T09:59:10Z", "i1", "e1")
	e2 := ent("2026-03-01T09:59:30Z", "2026-03-01T09:59:40Z", "i1", "e2")
	emitted, calls, _, _ := runFollower(t, []scriptedResult{
		page(e1),     // backlog
		page(e1, e2), // poll 1: e1 re-scanned, e2 new
		page(e1, e2), // poll 2: both old news
	}, 2, 5*time.Second, 0, 0)
	assertTexts(t, emitted, "e1", "e2")
	if len(calls) != 3 {
		t.Fatalf("fetch called %d times, want 3", len(calls))
	}
}

func TestFollowLateArrivalWithinLookbackEmitted(t *testing.T) {
	t.Parallel()
	// An entry ingested late but whose emission time is still inside the
	// lookback window shows up in a later re-scan and must be emitted.
	// Anything older than the window start is outside the request — the
	// documented, by-design miss — so the start assertion below is the
	// horizon's edge.
	eNew := ent("2026-03-01T09:59:00Z", "2026-03-01T09:59:10Z", "i1", "eNew")
	eLate := ent("2026-03-01T09:57:30Z", "2026-03-01T10:00:04Z", "i2", "eLate")
	emitted, calls, _, _ := runFollower(t, []scriptedResult{
		page(eNew),        // backlog
		page(eLate, eNew), // poll 1: eLate arrived late, emission time within lookback
		page(eLate, eNew), // poll 2
	}, 2, 5*time.Second, 0, 0)
	assertTexts(t, emitted, "eNew", "eLate")
	for i := 1; i <= 2; i++ {
		if want := "2026-03-01T09:57:00.000Z"; calls[i].start != want {
			t.Errorf("poll %d window start = %s, want %s (the lookback horizon)", i, calls[i].start, want)
		}
	}
}

func TestFollowWindowStartNeverBeforeSince(t *testing.T) {
	t.Parallel()
	// With a lookback reaching further back than --since, the poll window
	// clamps to since.
	e1 := ent("2026-03-01T09:55:00Z", "2026-03-01T09:55:10Z", "i1", "e1")
	_, calls, _, _ := runFollower(t, []scriptedResult{
		page(e1), // backlog
		page(),   // poll 1
	}, 1, 5*time.Second, 0, 30*time.Minute)
	if want := "2026-03-01T09:50:00.000Z"; calls[1].start != want {
		t.Errorf("poll window start = %s, want %s (clamped to since)", calls[1].start, want)
	}
}

func TestFollowEmptyPollsKeepWindow(t *testing.T) {
	t.Parallel()
	// Empty polls advance nothing: the window keeps sliding from the same
	// max seen emission time, and the seen set is retained.
	e1 := ent("2026-03-01T09:59:00Z", "2026-03-01T09:59:10Z", "i1", "e1")
	emitted, calls, _, f := runFollower(t, []scriptedResult{
		page(e1), // backlog
		page(),   // poll 1: nothing new
		page(),   // poll 2: nothing new
	}, 2, 5*time.Second, 0, 0)
	assertTexts(t, emitted, "e1")
	for i := 1; i <= 2; i++ {
		if want := "2026-03-01T09:57:00.000Z"; calls[i].start != want {
			t.Errorf(
				"poll %d window start = %s, want %s (max seen must not move on empty polls)",
				i,
				calls[i].start,
				want,
			)
		}
	}
	if _, ok := f.seen[DedupKey(e1)]; !ok {
		t.Error("seen set lost e1 across empty polls")
	}
}

func TestFollowSeenMapPruned(t *testing.T) {
	t.Parallel()
	// Entries falling behind the lookback horizon are pruned from the seen
	// map so memory stays bounded.
	eOld := ent("2026-03-01T09:51:00Z", "2026-03-01T09:51:10Z", "i1", "eOld")
	eNew := ent("2026-03-01T09:59:00Z", "2026-03-01T09:59:10Z", "i1", "eNew")
	emitted, _, _, f := runFollower(t, []scriptedResult{
		page(eOld), // backlog; max seen 09:51 → poll 1 window clamps to since
		page(eNew), // poll 1: max seen 09:59 → poll 2 window starts 09:57
		page(),     // poll 2: prune drops eOld (09:51 < 09:57 - 1s)
	}, 2, 5*time.Second, 0, 0)
	assertTexts(t, emitted, "eOld", "eNew")
	if len(f.seen) != 1 {
		t.Fatalf("seen map has %d keys after pruning, want 1: %v", len(f.seen), f.seen)
	}
	if _, ok := f.seen[DedupKey(eNew)]; !ok {
		t.Error("seen map pruned the wrong key: eNew should survive")
	}
}

func TestFollowRateLimitBacksOffThenRecovers(t *testing.T) {
	t.Parallel()
	e1 := ent("2026-03-01T09:59:00Z", "2026-03-01T09:59:10Z", "i1", "e1")
	e2 := ent("2026-03-01T10:00:20Z", "2026-03-01T10:00:21Z", "i1", "e2")
	rl := scriptedResult{err: clierr.New(clierr.CodeRateLimited, clierr.KindRateLimited, "429")}
	emitted, calls, sleeps, _ := runFollower(t, []scriptedResult{
		page(e1), // backlog
		rl,       // poll 1: rate limited -> poll doubles to 8s
		rl,       // poll 2: rate limited again -> 16s capped at 10s
		page(e2), // poll 3: success -> poll restores to 4s
		page(),   // poll 4: empty
	}, 4, 4*time.Second, 10*time.Second, 0)
	assertTexts(t, emitted, "e1", "e2")
	if len(calls) != 5 {
		t.Fatalf("fetch called %d times, want 5 (rate limiting must not exit the loop)", len(calls))
	}
	wantSleeps := []time.Duration{4 * time.Second, 8 * time.Second, 10 * time.Second, 4 * time.Second, 4 * time.Second}
	if len(sleeps) != len(wantSleeps) {
		t.Fatalf("slept %d times %v, want %d %v", len(sleeps), sleeps, len(wantSleeps), wantSleeps)
	}
	for i, want := range wantSleeps {
		if sleeps[i] != want {
			t.Fatalf("sleeps = %v, want %v", sleeps, wantSleeps)
		}
	}
}

func TestFollowCancelReturnsNil(t *testing.T) {
	t.Parallel()
	e1 := ent("2026-03-01T09:59:00Z", "2026-03-01T09:59:10Z", "i1", "e1")
	// allowed = 0: the very first sleep after the backlog is canceled.
	emitted, calls, _, _ := runFollower(t, []scriptedResult{page(e1)}, 0, 5*time.Second, 0, 0)
	assertTexts(t, emitted, "e1")
	if len(calls) != 1 {
		t.Fatalf("fetch called %d times, want 1 (backlog only)", len(calls))
	}
}

func TestFollowBacklogErrorIsFatal(t *testing.T) {
	t.Parallel()
	boom := clierr.New(clierr.CodeAuth, clierr.KindAuth, "key revoked")
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	clock := &fakeClock{now: followNow(), allowed: 10, cancel: cancel}
	var calls []fetchCall
	fetch := scriptFetcher([]scriptedResult{{err: boom}}, &calls)
	f := &Follower{Poll: 5 * time.Second, Clock: clock}
	var emitted []Entry
	err := f.Run(ctx, staticQuery(t), fetch, followSince(), 100, collectEmit(&emitted))
	if err != boom {
		t.Fatalf("Run error = %v, want the backlog fetch error", err)
	}
	if len(clock.sleeps) != 0 {
		t.Errorf("slept %d times before the backlog completed, want 0", len(clock.sleeps))
	}
}

func TestFollowNonRateLimitErrorExits(t *testing.T) {
	t.Parallel()
	e1 := ent("2026-03-01T09:59:00Z", "2026-03-01T09:59:10Z", "i1", "e1")
	boom := clierr.New(clierr.CodeAuth, clierr.KindAuth, "key revoked")
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	clock := &fakeClock{now: followNow(), allowed: 10, cancel: cancel}
	var calls []fetchCall
	fetch := scriptFetcher([]scriptedResult{page(e1), {err: boom}}, &calls)
	f := &Follower{Poll: 5 * time.Second, Clock: clock}
	var emitted []Entry
	err := f.Run(ctx, staticQuery(t), fetch, followSince(), 100, collectEmit(&emitted))
	if err != boom {
		t.Fatalf("Run error = %v, want the fetch error", err)
	}
}
