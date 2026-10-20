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
	q, err := Compile(&Filters{Group: "g"})
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	return q
}

// followCase is one Follower run on fakes: the pages it gets, how many sleeps it may take before it is canceled, its
// tuning, and how far each fetch moves the clock (a slow API).
type followCase struct {
	results                    []scriptedResult // the pages, in order, when all is nil
	all                        []Entry          // else every entry, ascending, served by realisticFetcher
	sleeps                     int
	poll, maxBackoff, lookback time.Duration
	limit                      int
	since                      time.Time // zero means followSince()
	perFetch                   time.Duration
}

// followRun is what runFollower saw: the entries emitted, the fetches made, the sleeps taken, the warnings, and the
// follower itself, so tests can inspect its seen map.
type followRun struct {
	emitted []Entry
	calls   []fetchCall
	sleeps  []time.Duration
	warned  []string
	f       *Follower
}

// runFollower wires a Follower with fakes and runs it to completion.
func runFollower(t *testing.T, fc *followCase) followRun {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var run followRun
	clock := &fakeClock{now: followNow(), allowed: fc.sleeps, cancel: cancel}
	served := scriptFetcher(fc.results, new([]fetchCall))
	if fc.all != nil {
		served = realisticFetcher(t, fc.all)
	}
	fetch := func(ctx context.Context, req Request) (*Page, *clierr.Error) {
		run.calls = append(run.calls, fetchCall{
			query: req.Query, start: req.Start, end: req.End, pageSize: req.PageSize, order: req.Order,
		})
		clock.now = clock.now.Add(fc.perFetch)
		return served(ctx, req)
	}
	since := fc.since
	if since.IsZero() {
		since = followSince()
	}
	run.f = &Follower{
		Poll: fc.poll, MaxBackoff: fc.maxBackoff, Lookback: fc.lookback, Limit: fc.limit, Clock: clock,
		Warn: func(l string) { run.warned = append(run.warned, l) },
	}
	if err := run.f.Run(ctx, staticQuery(t), fetch, since, 100, collectEmit(&run.emitted)); err != nil {
		t.Fatalf("Run returned %v, want nil on cancellation", err)
	}
	run.sleeps = clock.sleeps
	return run
}

func page(items ...Entry) scriptedResult {
	p := &Page{Items: items}
	if len(items) > 0 {
		p.PageMinTime = items[0].Time
		p.PageMaxTime = items[len(items)-1].Time
	}
	return scriptedResult{page: p}
}

// assertWindows checks each call's start and end against want, given as start, end pairs.
func assertWindows(t *testing.T, calls []fetchCall, want ...string) {
	t.Helper()
	if len(calls) != len(want)/2 {
		t.Fatalf("fetch called %d times, want %d", len(calls), len(want)/2)
	}
	for i := range calls {
		if calls[i].start != want[2*i] || calls[i].end != want[2*i+1] {
			t.Errorf(
				"call %d window = [%s, %s), want [%s, %s)",
				i,
				calls[i].start,
				calls[i].end,
				want[2*i],
				want[2*i+1],
			)
		}
	}
}

func TestFollowBacklogThenPolls(t *testing.T) {
	t.Parallel()
	e1 := ent("2026-03-01T09:51:00Z", "2026-03-01T09:51:30Z", "i1", "e1")
	e2 := ent("2026-03-01T09:55:00Z", "2026-03-01T09:55:30Z", "i1", "e2")
	e3 := ent("2026-03-01T09:59:59Z", "2026-03-01T10:00:02Z", "i1", "e3")
	e4 := ent("2026-03-01T10:00:16Z", "2026-03-01T10:00:17Z", "i1", "e4")
	run := runFollower(t, &followCase{
		results: []scriptedResult{
			page(e1, e2), // backlog
			page(e3),     // poll 1
			page(e4),     // poll 2
		},
		sleeps: 2,
	})
	emitted, calls := run.emitted, run.calls
	assertTexts(t, emitted, "e1", "e2", "e3", "e4")
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
	// The backlog spans [since, now); each poll (10s by default) reads
	// from 10s before the previous one to its own now.
	assertWindows(t, calls,
		"2026-03-01T09:50:00.000Z", "2026-03-01T10:00:00.000Z",
		"2026-03-01T09:59:50.000Z", "2026-03-01T10:00:10.000Z",
		"2026-03-01T10:00:00.000Z", "2026-03-01T10:00:20.000Z",
	)
}

func TestFollowRescanSkipsSeen(t *testing.T) {
	t.Parallel()
	// Every poll re-reads behind the previous one, so already-emitted
	// entries keep coming back; the seen set must swallow them.
	e1 := ent("2026-03-01T09:59:55Z", "2026-03-01T09:59:56Z", "i1", "e1")
	e2 := ent("2026-03-01T10:00:05Z", "2026-03-01T10:00:06Z", "i1", "e2")
	run := runFollower(t, &followCase{
		results: []scriptedResult{
			page(e1),     // backlog
			page(e1, e2), // poll 1: e1 read again, e2 new
			page(e1, e2), // poll 2: both old news
		},
		sleeps: 2,
	})
	assertTexts(t, run.emitted, "e1", "e2")
	if len(run.calls) != 3 {
		t.Fatalf("fetch called %d times, want 3", len(run.calls))
	}
}

// TestFollowSettleScanReadsEachAgedMinuteOnce: every minute, after that minute's poll, the settle scan reads the
// emission times that have aged past Lookback since it last read, picking up entries that arrived too late for the
// polls; consecutive settle scans meet without overlapping, and the first starts Lookback before the backlog.
func TestFollowSettleScanReadsEachAgedMinuteOnce(t *testing.T) {
	t.Parallel()
	eNew := ent("2026-03-01T09:59:59Z", "2026-03-01T10:00:00Z", "i1", "eNew")
	eLate := ent("2026-03-01T09:58:30Z", "2026-03-01T10:00:55Z", "i2", "eLate")
	results := []scriptedResult{
		page(eNew),                                     // backlog at 10:00:00
		page(), page(), page(), page(), page(), page(), // polls 1-6, 10:00:10 to 10:01:00
		page(eLate, eNew), // the first settle scan: eLate arrived 2m25s late
	}
	run := runFollower(t, &followCase{results: results, sleeps: 13})
	assertTexts(t, run.emitted, "eNew", "eLate")
	if len(run.calls) != 16 {
		t.Fatalf("fetch called %d times, want 16: the backlog, 13 polls and 2 settle scans", len(run.calls))
	}
	assertWindows(t, []fetchCall{run.calls[6], run.calls[7], run.calls[13], run.calls[14]},
		"2026-03-01T10:00:40.000Z", "2026-03-01T10:01:00.000Z", // poll 6 at 10:01:00
		"2026-03-01T09:58:00.000Z", "2026-03-01T09:59:00.000Z", // settle: the minute aged past 2m
		"2026-03-01T10:01:40.000Z", "2026-03-01T10:02:00.000Z", // poll 12 at 10:02:00
		"2026-03-01T09:59:00.000Z", "2026-03-01T10:00:00.000Z", // settle: the next minute
	)
}

// TestFollowOverrunSkipsSettleWithWarning: when a poll takes longer than the poll interval, the settle scan due
// after it is skipped, with one warning, so the polls keep up.
func TestFollowOverrunSkipsSettleWithWarning(t *testing.T) {
	t.Parallel()
	// Each fetch takes 15s against a 10s interval: polls at 10:00:25,
	// 10:00:50, 10:01:15 (settle due, skipped) and 10:01:40.
	run := runFollower(t, &followCase{sleeps: 4, perFetch: 15 * time.Second})
	if len(run.calls) != 5 {
		t.Fatalf("fetch called %d times, want 5 (the backlog and 4 polls, no settle scan)", len(run.calls))
	}
	if len(run.warned) != 1 || !strings.Contains(run.warned[0], "skipping the check") {
		t.Errorf("warned %v, want one warning about skipping the settle scan", run.warned)
	}
}

// TestFollowFailedSettleIsRetried: a rate-limited settle scan leaves what it was to read unread, so the next one
// starts from the same place.
func TestFollowFailedSettleIsRetried(t *testing.T) {
	t.Parallel()
	rl := scriptedResult{err: clierr.New(clierr.CodeRateLimited, clierr.KindRateLimited, "429")}
	run := runFollower(t, &followCase{
		results: []scriptedResult{
			page(), // backlog at 10:00:00
			page(), // poll 1 at 10:01:00
			rl,     // settle: rate limited
			page(), // poll 2 at 10:02:00
			page(), // settle again
		},
		sleeps: 2, poll: time.Minute, maxBackoff: time.Minute,
	})
	assertWindows(t, []fetchCall{run.calls[2], run.calls[4]},
		"2026-03-01T09:58:00.000Z", "2026-03-01T09:59:00.000Z",
		"2026-03-01T09:58:00.000Z", "2026-03-01T10:00:00.000Z",
	)
}

// TestFollowBacklogLimitKeepsTheNewest: with a limit the backlog is read newest first and its newest entries
// printed; the polls then start no earlier than the oldest one printed, so they never print what the limit left out.
func TestFollowBacklogLimitKeepsTheNewest(t *testing.T) {
	t.Parallel()
	run := runFollower(t, &followCase{
		all: []Entry{
			ent("2026-03-01T09:59:52Z", "r", "i1", "e1"),
			ent("2026-03-01T09:59:55Z", "r", "i1", "e2"),
			ent("2026-03-01T09:59:58Z", "r", "i1", "e3"),
			ent("2026-03-01T10:00:05Z", "r", "i1", "e4"), // after the backlog's end, so first read by poll 1
		},
		sleeps: 1, limit: 2,
	})
	assertTexts(t, run.emitted, "e2", "e3", "e4")
	if run.calls[0].order != Descending {
		t.Errorf("backlog order = %s, want desc", run.calls[0].order)
	}
	if want := "2026-03-01T09:59:55.000Z"; run.calls[len(run.calls)-1].start != want {
		t.Errorf("poll window start = %s, want %s (the oldest entry printed)", run.calls[len(run.calls)-1].start, want)
	}
}

// TestFollowFastClockEntryDoesNotMoveTheWindows: an entry stamped ahead of now (a node whose clock runs fast) must
// not pull the windows forward; they are measured from this machine's clock.
func TestFollowFastClockEntryDoesNotMoveTheWindows(t *testing.T) {
	t.Parallel()
	// Stamped 15s ahead of now: no window holds it until poll 2 (10:00:20).
	ahead := ent("2026-03-01T10:00:15Z", "2026-03-01T10:00:01Z", "i1", "ahead")
	run := runFollower(t, &followCase{all: []Entry{ahead}, sleeps: 2})
	assertTexts(t, run.emitted, "ahead")
	assertWindows(t, run.calls,
		"2026-03-01T09:50:00.000Z", "2026-03-01T10:00:00.000Z",
		"2026-03-01T09:59:50.000Z", "2026-03-01T10:00:10.000Z", // 10s before the previous poll, not the entry
		"2026-03-01T10:00:00.000Z", "2026-03-01T10:00:20.000Z",
	)
}

// TestFollowPollsStartToStart: the poll interval runs from one poll's start to the next, so the time a read takes
// comes out of the wait, and a read longer than the interval is followed at once.
func TestFollowPollsStartToStart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		perFetch  time.Duration
		wantSleep time.Duration
	}{
		{"a 4s read", 4 * time.Second, 6 * time.Second},
		{"a 15s read", 15 * time.Second, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := runFollower(t, &followCase{sleeps: 3, perFetch: tc.perFetch})
			for i, d := range run.sleeps {
				if d != tc.wantSleep {
					t.Fatalf("sleep %d = %s, want %s (sleeps %v)", i, d, tc.wantSleep, run.sleeps)
				}
			}
		})
	}
}

func TestFollowWindowStartNeverBeforeSince(t *testing.T) {
	t.Parallel()
	// With --since closer than the overlap, the poll window clamps to it.
	run := runFollower(t, &followCase{sleeps: 1, since: followNow().Add(-5 * time.Second)})
	if want := "2026-03-01T09:59:55.000Z"; run.calls[1].start != want {
		t.Errorf("poll window start = %s, want %s (clamped to since)", run.calls[1].start, want)
	}
}

func TestFollowEmptyPollsKeepSeen(t *testing.T) {
	t.Parallel()
	// Empty polls still move the windows on with the clock, and the seen
	// set keeps what the next settle scan returns again.
	e1 := ent("2026-03-01T09:59:00Z", "2026-03-01T09:59:10Z", "i1", "e1")
	run := runFollower(t, &followCase{results: []scriptedResult{page(e1), page(), page()}, sleeps: 2})
	assertTexts(t, run.emitted, "e1")
	if want := "2026-03-01T10:00:00.000Z"; run.calls[2].start != want {
		t.Errorf("poll 2 window start = %s, want %s (10s before poll 1)", run.calls[2].start, want)
	}
	if _, ok := run.f.seen[DedupKey(&e1)]; !ok {
		t.Error("seen set lost e1, which the next settle scan (from 09:58) returns again")
	}
}

func TestFollowSeenMapPruned(t *testing.T) {
	t.Parallel()
	// Entries behind every later window's start are pruned from the seen
	// map so memory stays bounded.
	eOld := ent("2026-03-01T09:51:00Z", "2026-03-01T09:51:10Z", "i1", "eOld")
	eNew := ent("2026-03-01T09:59:59Z", "2026-03-01T10:00:00Z", "i1", "eNew")
	run := runFollower(t, &followCase{
		results: []scriptedResult{
			page(eOld), // backlog
			page(eNew), // poll 1: no later read starts before 09:58 (the settle scan's start), so eOld goes
			page(),     // poll 2
		},
		sleeps: 2,
	})
	assertTexts(t, run.emitted, "eOld", "eNew")
	if len(run.f.seen) != 1 {
		t.Fatalf("seen map has %d keys after pruning, want 1: %v", len(run.f.seen), run.f.seen)
	}
	if _, ok := run.f.seen[DedupKey(&eNew)]; !ok {
		t.Error("seen map pruned the wrong key: eNew should survive")
	}
}

func TestFollowRateLimitBacksOffThenRecovers(t *testing.T) {
	t.Parallel()
	e1 := ent("2026-03-01T09:59:00Z", "2026-03-01T09:59:10Z", "i1", "e1")
	e2 := ent("2026-03-01T10:00:20Z", "2026-03-01T10:00:21Z", "i1", "e2")
	rl := scriptedResult{err: clierr.New(clierr.CodeRateLimited, clierr.KindRateLimited, "429")}
	run := runFollower(t, &followCase{
		results: []scriptedResult{
			page(e1), // backlog
			rl,       // poll 1: rate limited -> poll doubles to 10s
			rl,       // poll 2: rate limited again -> 20s capped at 12s
			page(e2), // poll 3: success -> poll restores to 5s
			page(),   // poll 4: empty
		},
		sleeps: 4, poll: 5 * time.Second, maxBackoff: 12 * time.Second,
	})
	assertTexts(t, run.emitted, "e1", "e2")
	if len(run.calls) != 5 {
		t.Fatalf("fetch called %d times, want 5 (rate limiting must not exit the loop)", len(run.calls))
	}
	wantSleeps := []time.Duration{5 * time.Second, 10 * time.Second, 12 * time.Second, 5 * time.Second, 5 * time.Second}
	if len(run.sleeps) != len(wantSleeps) {
		t.Fatalf("slept %d times %v, want %d %v", len(run.sleeps), run.sleeps, len(wantSleeps), wantSleeps)
	}
	for i, want := range wantSleeps {
		if run.sleeps[i] != want {
			t.Fatalf("sleeps = %v, want %v", run.sleeps, wantSleeps)
		}
	}
	// The polls that failed moved nothing on: poll 3 still reads from 10s
	// before the backlog, so it covers what they missed.
	if want := "2026-03-01T09:59:50.000Z"; run.calls[3].start != want {
		t.Errorf("poll 3 window start = %s, want %s", run.calls[3].start, want)
	}
}

func TestFollowCancelReturnsNil(t *testing.T) {
	t.Parallel()
	e1 := ent("2026-03-01T09:59:00Z", "2026-03-01T09:59:10Z", "i1", "e1")
	// No sleeps allowed: the very first sleep after the backlog is canceled.
	run := runFollower(t, &followCase{results: []scriptedResult{page(e1)}})
	assertTexts(t, run.emitted, "e1")
	if len(run.calls) != 1 {
		t.Fatalf("fetch called %d times, want 1 (backlog only)", len(run.calls))
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

// TestFollowerSettings: unset tuning takes the defaults, a poll below MinPoll is floored, and a nil Clock is the
// real one.
func TestFollowerSettings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                       string
		f                          *Follower
		poll, maxBackoff, lookback time.Duration
	}{
		{"zero", &Follower{}, DefaultPoll, DefaultMaxBackoff, DefaultLookback},
		{"poll below the floor", &Follower{Poll: time.Second}, MinPoll, DefaultMaxBackoff, DefaultLookback},
		{
			"negative backoff and lookback",
			&Follower{Poll: 6 * time.Second, MaxBackoff: -time.Second, Lookback: -time.Second},
			6 * time.Second, DefaultMaxBackoff, DefaultLookback,
		},
		{
			"all set",
			&Follower{Poll: 10 * time.Second, MaxBackoff: 20 * time.Second, Lookback: time.Minute},
			10 * time.Second, 20 * time.Second, time.Minute,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := tc.f.settings()
			if s.poll != tc.poll || s.maxBackoff != tc.maxBackoff || s.lookback != tc.lookback {
				t.Errorf("settings = poll %s, max backoff %s, lookback %s; want %s, %s, %s",
					s.poll, s.maxBackoff, s.lookback, tc.poll, tc.maxBackoff, tc.lookback)
			}
			if _, ok := s.clock.(RealClock); !ok {
				t.Errorf("clock = %T, want RealClock", s.clock)
			}
		})
	}
}

// TestFollowBackoffDoublesToTheCap: each rate-limited poll doubles the interval until it reaches MaxBackoff.
func TestFollowBackoffDoublesToTheCap(t *testing.T) {
	t.Parallel()
	s := followSettings{maxBackoff: 25 * time.Second}
	for _, tc := range []struct{ d, want time.Duration }{
		{5 * time.Second, 10 * time.Second},
		{10 * time.Second, 20 * time.Second},
		{20 * time.Second, 25 * time.Second},
		{25 * time.Second, 25 * time.Second},
	} {
		if got := s.backoff(tc.d); got != tc.want {
			t.Errorf("backoff(%s) = %s, want %s", tc.d, got, tc.want)
		}
	}
}
