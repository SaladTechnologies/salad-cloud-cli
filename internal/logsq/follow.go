package logsq

import (
	"context"
	"fmt"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// Clock abstracts time for the follow loop so tests run without real sleeps.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock is the production Clock.
type RealClock struct{}

// Now returns the system time.
func (RealClock) Now() time.Time { return time.Now() }

// Sleep waits for d or until ctx is done, returning ctx.Err() in that case.
func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("sleep cut short: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}

// Follow-loop tuning.
const (
	DefaultPoll       = 10 * time.Second
	MinPoll           = 5 * time.Second
	DefaultMaxBackoff = 60 * time.Second

	// DefaultLookback is how long after its emission time an entry can be
	// ingested and still be printed (see Follower.Lookback).
	DefaultLookback = 2 * time.Minute

	// pollOverlap is how far before the previous poll each poll reads: an
	// entry ingested up to this long after its emission time is printed by
	// the next poll. Most entries are ingested within seconds.
	pollOverlap = 10 * time.Second

	// settleEvery is how often the settle scan runs: it reads, once, the
	// emission times that have aged past Lookback since it last ran, and
	// prints the entries that arrived too late for the polls.
	settleEvery = time.Minute

	// seenPruneGrace keeps seen keys slightly past the re-scan horizon so
	// an entry sitting exactly on a window's start edge cannot be
	// re-emitted by a later poll.
	seenPruneGrace = time.Second
)

// Follower tails logs by polling windows on `time` (node emission time)
// with client-side dedup.
//
// This design is forced by the API: the start/end window binds to `time`,
// not receive_time, and receive_time is not a queryable field (the query
// engine rejects it, whatever the public docs say). A receive_time
// watermark is therefore impossible; the only way to catch late-arriving
// entries is to read behind them again and suppress repeats by identity.
//
// Each poll reads from pollOverlap before the previous poll, so an entry
// up to pollOverlap late prints with the next poll. Every settleEvery the
// settle scan reads the emission times that have aged past Lookback since
// it last ran: by then every entry up to Lookback late has arrived, so it
// prints them once each, about Lookback after the rest. Each moment of
// logs is read about three times in all at the default 10s poll (the poll
// that first covers it, the next poll's overlap and the settle scan), not
// once per poll. The honest consequence: an entry ingested more than
// Lookback late is missed.
//
// A sequential reader can only read so fast (a page per request), so at a
// high log rate a poll can take longer than the poll interval. The settle
// scan is then skipped, with a warning, so that the polls keep up with new
// entries; late entries arriving in the meantime may be missed.
//
// Each window ends at the moment its read starts, and the next one picks up
// pollOverlap before that: a window open past now made a slow poll chase
// entries as they arrived, and the next poll read them all again. An entry
// a fast node clock stamps ahead of now is printed once a window's end
// passes its time.
//
// The windows are measured from this machine's clock, never from the
// entries: a node whose clock runs fast stamps entries ahead of now, and a
// window anchored on the newest entry seen was pulled forward by them,
// skipping the on-time entries behind. Polls start every Poll, measured
// start to start, so a poll that ran long is followed at once by the next.
type Follower struct {
	Poll       time.Duration     // effective poll interval; 0 means DefaultPoll, floored at MinPoll
	MaxBackoff time.Duration     // rate-limit backoff cap; 0 means DefaultMaxBackoff
	Lookback   time.Duration     // how late an entry can be ingested and still print; 0 means DefaultLookback
	Limit      int               // how many of the backlog's newest entries to print; 0 means all
	Clock      Clock             // nil means RealClock
	Warn       func(line string) // where warning: lines go; nil drops them

	// seen maps the DedupKey of every emitted entry inside the re-scan
	// horizon to its parsed emission time, so re-scans emit nothing twice.
	// It is pruned after each poll and exposed on the struct for tests.
	seen map[string]time.Time
}

// Run tails logs from since until ctx is canceled (which returns nil — a
// clean exit). It first prints the backlog [since, now), or its newest
// Limit entries, then polls windows ending at each poll's now with the
// same static query, emitting only entries not already seen. Backlog
// errors are fatal; rate-limited polls back off (doubling to MaxBackoff)
// and never exit; other poll errors are fatal.
func (f *Follower) Run(
	ctx context.Context,
	query string,
	fetch Fetcher,
	since time.Time,
	pageSize int,
	emit func(Entry) error,
) *clierr.Error {
	cfg := f.settings()
	f.seen = make(map[string]time.Time)
	loop := &followLoop{
		query:       query,
		fetch:       fetch,
		pageSize:    pageSize,
		limit:       f.Limit,
		emit:        emit,
		warn:        f.Warn,
		cfg:         cfg,
		warnedSkips: false,
		st: &followState{
			seen:       f.seen,
			since:      since.UTC(),
			lastPoll:   time.Time{},
			lastSettle: time.Time{},
			settled:    time.Time{},
		},
	}
	started := cfg.clock.Now().UTC()
	if werr := loop.backlog(ctx, started); werr != nil {
		if ctx.Err() != nil {
			return nil //nolint:nilerr // canceled: --follow ends with exit 0
		}
		return werr
	}
	interval := cfg.poll
	next := started.Add(interval) // polls start every interval, start to start
	for {
		if err := cfg.clock.Sleep(ctx, max(next.Sub(cfg.clock.Now().UTC()), 0)); err != nil {
			return nil //nolint:nilerr // canceled: --follow ends with exit 0
		}
		started = cfg.clock.Now().UTC()
		switch werr := loop.tick(ctx, started); {
		case werr == nil:
			interval = cfg.poll // restore the configured interval after a success
			next = started.Add(interval)
		case ctx.Err() != nil:
			return nil //nolint:nilerr // canceled: --follow ends with exit 0
		case werr.Code != clierr.CodeRateLimited:
			return werr
		default:
			interval = cfg.backoff(interval)
			next = cfg.clock.Now().UTC().Add(interval) // a rate-limited poll waits its whole backoff
			warn(f.Warn, fmt.Sprintf("warning: rate limited by the API; slowing polling to %s", interval))
		}
	}
}

// followSettings are a Follower's tuning with the defaults and the poll floor applied.
type followSettings struct {
	poll, maxBackoff, lookback time.Duration
	clock                      Clock
}

// settings resolves f's tuning: a zero Poll is DefaultPoll and any poll is floored at MinPoll, a MaxBackoff or
// Lookback of zero or less is its default, and a nil Clock is RealClock.
func (f *Follower) settings() followSettings {
	s := followSettings{poll: f.Poll, maxBackoff: f.MaxBackoff, lookback: f.Lookback, clock: f.Clock}
	if s.poll == 0 {
		s.poll = DefaultPoll
	}
	s.poll = max(s.poll, MinPoll)
	if s.maxBackoff <= 0 {
		s.maxBackoff = DefaultMaxBackoff
	}
	if s.lookback <= 0 {
		s.lookback = DefaultLookback
	}
	if s.clock == nil {
		s.clock = RealClock{}
	}
	return s
}

// backoff is the poll interval after a rate-limited poll that waited d: double d, capped at maxBackoff.
func (s followSettings) backoff(d time.Duration) time.Duration {
	d *= 2
	if d > s.maxBackoff {
		d = s.maxBackoff
	}
	return d
}

// followLoop is one Run: what it fetches and prints, its tuning, whether it has warned about skipping the settle
// scan, and its state.
type followLoop struct {
	query       string
	fetch       Fetcher
	pageSize    int
	limit       int
	emit        func(Entry) error
	warn        func(string)
	cfg         followSettings
	warnedSkips bool
	st          *followState
}

// backlog prints the backlog [since, now), or its newest limit entries, which then become where the polls start: a
// poll never prints an entry older than the backlog left out. Everything older than lookback before now counts as
// settled.
func (l *followLoop) backlog(ctx context.Context, now time.Time) *clierr.Error {
	end := now
	w := Window{Query: l.query, Start: l.st.since, End: end, PageSize: l.pageSize}
	if l.limit == 0 {
		if werr := FetchWindow(ctx, l.fetch, w, l.st.emitAll(l.emit, end), l.warn); werr != nil {
			return werr
		}
	} else if werr := l.backlogNewest(ctx, w); werr != nil {
		return werr
	}
	l.st.lastPoll, l.st.lastSettle = now, now
	l.st.settled = latest(l.st.since, now.Add(-l.cfg.lookback))
	return nil
}

// backlogNewest prints the newest limit entries of w and, when older ones were left out, moves since up to the
// oldest one printed.
func (l *followLoop) backlogNewest(ctx context.Context, w Window) *clierr.Error {
	var oldest time.Time
	record := l.st.emitAll(l.emit, w.End)
	truncated, werr := FetchNewest(ctx, l.fetch, w, l.limit, func(e Entry) error {
		if oldest.IsZero() { // entries come oldest first
			if t, err := time.Parse(time.RFC3339Nano, e.Time); err == nil {
				oldest = t.UTC()
			}
		}
		return record(e)
	}, l.warn)
	if werr != nil {
		return werr
	}
	if truncated && !oldest.IsZero() {
		l.st.since = latest(l.st.since, oldest)
	}
	return nil
}

// tick is one poll at now and, once a settleEvery has passed, the settle scan; a poll that took longer than the
// poll interval skips the settle scan instead.
func (l *followLoop) tick(ctx context.Context, now time.Time) *clierr.Error {
	if werr := l.poll(ctx, now); werr != nil {
		return werr
	}
	if now.Sub(l.st.lastSettle) < settleEvery {
		return nil
	}
	if took := l.cfg.clock.Now().UTC().Sub(now); took > l.cfg.poll {
		l.skipSettle(now, took)
		return nil
	}
	return l.settle(ctx, now)
}

// poll reads from pollOverlap before the previous poll to now and prints the entries no earlier read printed. Only a
// successful poll moves the state on, so after a failed one the next covers what it missed.
func (l *followLoop) poll(ctx context.Context, now time.Time) *clierr.Error {
	start := latest(l.st.lastPoll.Add(-pollOverlap), l.st.since)
	end := now
	werr := FetchWindow(
		ctx,
		l.fetch,
		Window{Query: l.query, Start: start, End: end, PageSize: l.pageSize},
		l.st.emitUnseen(l.emit, end),
		l.warn,
	)
	if werr != nil {
		return werr
	}
	l.st.lastPoll = now
	l.st.prune(l.st.horizon().Add(-seenPruneGrace))
	return nil
}

// settle reads the emission times that have aged past lookback since the last settle scan and prints the entries no
// earlier read printed: the ones that arrived too late for the polls.
func (l *followLoop) settle(ctx context.Context, now time.Time) *clierr.Error {
	end := now.Add(-l.cfg.lookback)
	if end.After(l.st.settled) {
		werr := FetchWindow(
			ctx,
			l.fetch,
			Window{Query: l.query, Start: l.st.settled, End: end, PageSize: l.pageSize},
			l.st.emitUnseen(l.emit, end),
			l.warn,
		)
		if werr != nil {
			return werr
		}
		l.st.settled = end
	}
	l.st.lastSettle = now
	l.st.prune(l.st.horizon().Add(-seenPruneGrace))
	return nil
}

// skipSettle gives up the settle scan due at now, after a poll that took took: the emission times it would have
// read count as settled, and the first skip says so on stderr.
func (l *followLoop) skipSettle(now time.Time, took time.Duration) {
	l.st.settled = latest(l.st.settled, now.Add(-l.cfg.lookback))
	l.st.lastSettle = now
	if !l.warnedSkips {
		l.warnedSkips = true
		warn(l.warn, fmt.Sprintf(
			"warning: a poll took %s, longer than --poll-interval %s, at this log volume; skipping the check "+
				"for entries that arrive more than %s late while polls run long, so some may be missed",
			took.Round(time.Second), l.cfg.poll, pollOverlap,
		))
	}
}

// followState is one Run's dedup state and what its windows start from: the emitted entries inside the re-scan
// horizon (the Follower's seen map), since, when the last successful poll and settle scan ran, and the emission time
// up to which the settle scan has read.
type followState struct {
	seen                 map[string]time.Time
	since                time.Time
	lastPoll, lastSettle time.Time
	settled              time.Time
}

// horizon is the earliest a later window can start: pollOverlap before the last poll, or where the settle scan reads
// from, but never before since.
func (s *followState) horizon() time.Time {
	return latest(earliest(s.lastPoll.Add(-pollOverlap), s.settled), s.since)
}

// record remembers an emitted entry. fallback stands in for an unparseable emission time (the window's end, so the
// entry is kept a full horizon).
func (s *followState) record(e *Entry, fallback time.Time) {
	t, err := time.Parse(time.RFC3339Nano, e.Time)
	if err != nil {
		t = fallback
	}
	s.seen[DedupKey(e)] = t.UTC()
}

// emitAll is the backlog's emit: it records and emits every entry of a window ending at end.
func (s *followState) emitAll(emit func(Entry) error, end time.Time) func(Entry) error {
	return func(e Entry) error {
		s.record(&e, end)
		return emit(e)
	}
}

// emitUnseen is a poll's emit: it records and emits the entries of a window ending at end that no earlier read
// emitted.
func (s *followState) emitUnseen(emit func(Entry) error, end time.Time) func(Entry) error {
	return func(e Entry) error {
		if _, dup := s.seen[DedupKey(&e)]; dup {
			return nil // already emitted in an earlier read
		}
		s.record(&e, end)
		return emit(e)
	}
}

// prune forgets the seen entries emitted before cutoff.
func (s *followState) prune(cutoff time.Time) {
	for k, t := range s.seen {
		if t.Before(cutoff) {
			delete(s.seen, k)
		}
	}
}

// earliest is the earlier of a and b.
func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// latest is the later of a and b.
func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
