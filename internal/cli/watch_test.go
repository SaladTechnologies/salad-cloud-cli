package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// fakeWatchDeps returns deps with a fixed clock and a timer channel the
// test fires by hand.
func fakeWatchDeps(out *bytes.Buffer, tty bool, tick chan time.Time) watchDeps {
	return watchDeps{
		out:     out,
		errOut:  out,
		tty:     tty,
		now:     func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) },
		after:   func(time.Duration) <-chan time.Time { return tick },
		refresh: 5 * time.Second,
	}
}

func TestWatchLoopStopsCleanlyOnCancel(t *testing.T) {
	var out bytes.Buffer
	tick := make(chan time.Time)
	ctx, cancel := context.WithCancel(t.Context())
	renders := 0
	done := make(chan error)
	go func() {
		done <- watchLoop(ctx, fakeWatchDeps(&out, false, tick), func(context.Context) *clierr.Error {
			renders++
			return nil
		})
	}()
	tick <- time.Time{} // second render
	tick <- time.Time{} // third render
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("watchLoop = %v, want nil on cancel (Ctrl-C is the normal stop)", err)
	}
	if renders != 3 {
		t.Errorf("renders = %d, want 3", renders)
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("wrote VT codes to a non-terminal: %q", out.String())
	}
	if !strings.Contains(out.String(), "Every 5s, at 12:00:00") || strings.Count(out.String(), "\n\nEvery") != 2 {
		t.Errorf("renders not separated by a blank line: %q", out.String())
	}
}

func TestWatchLoopClearsATerminal(t *testing.T) {
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_ = watchLoop(ctx, fakeWatchDeps(&out, true, nil), func(context.Context) *clierr.Error { return nil })
	if !strings.HasPrefix(out.String(), "\x1b[2J\x1b[H") {
		t.Errorf("terminal render did not clear the screen: %q", out.String())
	}
}

func TestWatchLoopEndsOnFatalError(t *testing.T) {
	var out bytes.Buffer
	err := watchLoop(t.Context(), fakeWatchDeps(&out, false, nil), func(context.Context) *clierr.Error {
		return clierr.New(clierr.CodeNotFound, clierr.KindNotFound, "gone")
	})
	if cerr, ok := err.(*clierr.Error); !ok || cerr.Code != clierr.CodeNotFound {
		t.Errorf("watchLoop = %v, want the not-found error", err)
	}
}

func TestWatchIsTableModeOnly(t *testing.T) {
	_, err := runRoot(t, "container-groups", "list", "-w", "-o", "json")
	cerr, ok := err.(*clierr.Error)
	if !ok || cerr.Code != clierr.CodeUsage || !strings.Contains(cerr.Message, "--wait") {
		t.Errorf("list -w -o json = %v, want a usage error pointing at --wait", err)
	}
}
