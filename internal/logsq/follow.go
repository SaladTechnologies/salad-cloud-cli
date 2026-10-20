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
	DefaultPoll       = 5 * time.Second
	MinPoll           = 2 * time.Second
	DefaultMaxBackoff = 60 * time.Second

	// DefaultLookback is how far behind the newest seen emission time each
	// poll re-scans (see Follower.Lookback).
	DefaultLookback = 2 * time.Minute

	// followLookahead widens each window's end past now, so entries with
	// slightly-fast node clocks are not excluded.
	followLookahead = time.Minute

	// seenPruneGrace keeps seen keys slightly past the lookback horizon so
	// an entry sitting exactly on a window's start edge cannot be
	// re-emitted by the next poll.
	seenPruneGrace = time.Second
)

// Follower tails logs by polling a sliding lookback window on `time` (node
// emission time) with client-side dedup.
//
// This design is forced by the API: the start/end window binds to `time`,
// not receive_time, and receive_time is not a queryable field (the query
// engine rejects it, whatever the public docs say). A receive_time
// watermark is therefore impossible; the only way to catch late-arriving
// entries is to re-scan a horizon behind the newest emission time seen and
// suppress repeats by identity. The honest consequence: an entry ingested
// more than Lookback after its node-emission time is missed by --follow.
type Follower struct {
	Poll       time.Duration     // effective poll interval; 0 means DefaultPoll, floored at MinPoll
	MaxBackoff time.Duration     // rate-limit backoff cap; 0 means DefaultMaxBackoff
	Lookback   time.Duration     // re-scan horizon behind max seen `time`; 0 means DefaultLookback
	Clock      Clock             // nil means RealClock
	Warn       func(line string) // where warning: lines go; nil drops them

	// seen maps the DedupKey of every emitted entry inside the lookback
	// horizon to its parsed emission time, so re-scans emit nothing twice.
	// It is pruned after each poll and exposed on the struct for tests.
	seen map[string]time.Time
}

// Run tails logs from since until ctx is canceled (which returns nil — a
// clean exit). It first drains the backlog [since, now+lookahead) emitting
// everything, then polls [maxSeenTime-Lookback, now+lookahead) with the
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
	st := &followState{seen: f.seen, maxSeen: time.Time{}, hasSeen: false}
	sinceU := since.UTC()

	// Backlog: everything already in [since, now+lookahead).
	backlogEnd := cfg.clock.Now().UTC().Add(followLookahead)
	werr := FetchWindow(
		ctx,
		fetch,
		Window{Query: query, Start: sinceU, End: backlogEnd, PageSize: pageSize},
		st.emitAll(emit, backlogEnd),
		f.Warn,
	)
	if werr != nil {
		if ctx.Err() != nil {
			return nil //nolint:nilerr // canceled: --follow ends with exit 0
		}
		return werr
	}

	effective := cfg.poll
	for {
		if err := cfg.clock.Sleep(ctx, effective); err != nil {
			return nil //nolint:nilerr // canceled: --follow ends with exit 0
		}
		windowStart := st.windowStart(sinceU, cfg.lookback)
		windowEnd := cfg.clock.Now().UTC().Add(followLookahead)
		werr = FetchWindow(
			ctx,
			fetch,
			Window{Query: query, Start: windowStart, End: windowEnd, PageSize: pageSize},
			st.emitUnseen(emit, windowEnd),
			f.Warn,
		)
		if werr != nil {
			if ctx.Err() != nil {
				return nil //nolint:nilerr // canceled: --follow ends with exit 0
			}
			if werr.Code != clierr.CodeRateLimited {
				return werr
			}
			effective = cfg.backoff(effective)
			warn(f.Warn, fmt.Sprintf("warning: rate limited by the API; slowing polling to %s", effective))
			continue
		}
		effective = cfg.poll // restore the configured interval after a success
		// Prune seen keys that have fallen behind the lookback horizon:
		// future windows start no earlier than this one did, so anything
		// older than windowStart (minus a grace tick) can never reappear.
		st.prune(windowStart.Add(-seenPruneGrace))
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

// followState is one Run's dedup state: the emitted entries inside the lookback horizon (the Follower's seen map)
// and the newest parsed emission time among them.
type followState struct {
	seen    map[string]time.Time
	maxSeen time.Time
	hasSeen bool
}

// record remembers an emitted entry. fallback stands in for an unparseable emission time (the window's end:
// retained a full horizon, but never allowed to drag maxSeen forward).
func (s *followState) record(e *Entry, fallback time.Time) {
	key := DedupKey(e)
	t, err := time.Parse(time.RFC3339Nano, e.Time)
	if err != nil {
		s.seen[key] = fallback
		return
	}
	t = t.UTC()
	s.seen[key] = t
	if !s.hasSeen || t.After(s.maxSeen) {
		s.maxSeen, s.hasSeen = t, true
	}
}

// emitAll is the backlog's emit: it records and emits every entry of a window ending at end.
func (s *followState) emitAll(emit func(Entry) error, end time.Time) func(Entry) error {
	return func(e Entry) error {
		s.record(&e, end)
		return emit(e)
	}
}

// emitUnseen is a poll's emit: it records and emits the entries of a window ending at end that no earlier scan
// emitted.
func (s *followState) emitUnseen(emit func(Entry) error, end time.Time) func(Entry) error {
	return func(e Entry) error {
		if _, dup := s.seen[DedupKey(&e)]; dup {
			return nil // already emitted in an earlier scan
		}
		s.record(&e, end)
		return emit(e)
	}
}

// windowStart is where the next poll's window starts: lookback behind the newest emission time seen, but never
// before since.
func (s *followState) windowStart(since time.Time, lookback time.Duration) time.Time {
	if s.hasSeen {
		if ws := s.maxSeen.Add(-lookback); ws.After(since) {
			return ws
		}
	}
	return since
}

// prune forgets the seen entries emitted before cutoff.
func (s *followState) prune(cutoff time.Time) {
	for k, t := range s.seen {
		if t.Before(cutoff) {
			delete(s.seen, k)
		}
	}
}
