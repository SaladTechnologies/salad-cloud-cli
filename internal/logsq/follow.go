package logsq

import (
	"context"
	"fmt"
	"os"
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
		return ctx.Err()
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
	Poll       time.Duration // effective poll interval; 0 means DefaultPoll, floored at MinPoll
	MaxBackoff time.Duration // rate-limit backoff cap; 0 means DefaultMaxBackoff
	Lookback   time.Duration // re-scan horizon behind max seen `time`; 0 means DefaultLookback
	Clock      Clock         // nil means RealClock

	// seen maps the DedupKey of every emitted entry inside the lookback
	// horizon to its parsed emission time, so re-scans emit nothing twice.
	// It is pruned after each poll and exposed on the struct for tests.
	seen map[string]time.Time
}

// Run tails logs from since until ctx is cancelled (which returns nil — a
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
	poll := f.Poll
	if poll == 0 {
		poll = DefaultPoll
	}
	if poll < MinPoll {
		poll = MinPoll
	}
	maxBackoff := f.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = DefaultMaxBackoff
	}
	lookback := f.Lookback
	if lookback <= 0 {
		lookback = DefaultLookback
	}
	clock := f.Clock
	if clock == nil {
		clock = RealClock{}
	}

	f.seen = map[string]time.Time{}
	sinceU := since.UTC()
	var maxSeen time.Time
	hasSeen := false

	// record remembers an emitted entry. fallback stands in for an
	// unparseable emission time (the window's end: retained a full horizon,
	// but never allowed to drag maxSeen forward).
	record := func(e Entry, fallback time.Time) {
		key := DedupKey(e)
		t, err := time.Parse(time.RFC3339Nano, e.Time)
		if err != nil {
			f.seen[key] = fallback
			return
		}
		t = t.UTC()
		f.seen[key] = t
		if !hasSeen || t.After(maxSeen) {
			maxSeen, hasSeen = t, true
		}
	}

	// Backlog: everything already in [since, now+lookahead).
	backlogEnd := clock.Now().UTC().Add(followLookahead)
	if werr := FetchWindow(ctx, fetch, query, sinceU, backlogEnd, pageSize, func(e Entry) error {
		record(e, backlogEnd)
		return emit(e)
	}); werr != nil {
		if ctx.Err() != nil {
			return nil //nolint:nilerr // cancelled: --follow ends with exit 0
		}
		return werr
	}

	effective := poll
	for {
		if err := clock.Sleep(ctx, effective); err != nil {
			return nil //nolint:nilerr // cancelled: --follow ends with exit 0
		}
		windowStart := sinceU
		if hasSeen {
			if ws := maxSeen.Add(-lookback); ws.After(sinceU) {
				windowStart = ws
			}
		}
		windowEnd := clock.Now().UTC().Add(followLookahead)
		werr := FetchWindow(ctx, fetch, query, windowStart, windowEnd, pageSize, func(e Entry) error {
			if _, dup := f.seen[DedupKey(e)]; dup {
				return nil // already emitted in an earlier scan
			}
			record(e, windowEnd)
			return emit(e)
		})
		if werr != nil {
			if ctx.Err() != nil {
				return nil //nolint:nilerr // cancelled: --follow ends with exit 0
			}
			if werr.Code == clierr.CodeRateLimited {
				effective *= 2
				if effective > maxBackoff {
					effective = maxBackoff
				}
				fmt.Fprintf(os.Stderr, "warning: rate limited by the API; slowing polling to %s\n", effective)
				continue
			}
			return werr
		}
		effective = poll // restore the configured interval after a success
		// Prune seen keys that have fallen behind the lookback horizon:
		// future windows start no earlier than this one did, so anything
		// older than windowStart (minus a grace tick) can never reappear.
		cutoff := windowStart.Add(-seenPruneGrace)
		for k, t := range f.seen {
			if t.Before(cutoff) {
				delete(f.seen, k)
			}
		}
	}
}
