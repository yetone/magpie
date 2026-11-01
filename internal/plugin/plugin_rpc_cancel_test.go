package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestOrdinaryCallCancelTellsTheHost: a caller whose context ends while an
// ordinary RPC is under way has the host told of it, as a fetch's or a
// gateway request's caller is. Before, only those two kinds were
// cancellable: the plugin's usage or model work went on unseen, and repeated
// reads left task after task alive past their callers (#4678).
func TestOrdinaryCallCancelTellsTheHost(t *testing.T) {
	f := newFakeHost(t)
	// the call count is read beside a caller goroutine still admitting, so
	// it is read under the host's lock
	liveCalls := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.host.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan error, 1)
	go func() {
		waited <- f.host.call(ctx, "usage", map[string]any{"provider": "fakeco"}, nil)
	}()
	waitFor(t, "the usage call reached the host", func() bool { return liveCalls() == 1 })
	cancel()
	if err := <-waited; !errors.Is(err, context.Canceled) {
		t.Fatalf("usage = %v, want canceled", err)
	}
	waitFor(t, "the host was told of the cancellation", func() bool {
		for _, m := range f.methods() {
			if m == "abort" {
				return true
			}
		}
		return false
	})
	waitFor(t, "the aborted call left the host", func() bool { return liveCalls() == 0 })
	waitFor(t, "the call's slot came free", func() bool { return f.slotsFree() == cap(f.host.slots) })
}

// TestCancelledUsageReachesThePlugin: a usage read whose caller gave up has
// the plugin's own usage hook told as well, through the signal the host
// hands it, so a vendor read that supports stopping stops instead of waiting
// out its caller's successor (#4678). The hook traces what it was handed
// and whether the abort arrived, to a file the test reads.
func TestCancelledUsageReachesThePlugin(t *testing.T) {
	sandbox(t)
	trace := filepath.Join(t.TempDir(), "trace.jsonl")
	t.Setenv("STUCK_USAGE", "1")
	t.Setenv("STUCK_TRACE", trace)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	abs, err := filepath.Abs("testdata/stuck/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := APIKey(ctx, "stuckco", 0, nil, "k-stuckco", NewAccount); err != nil {
		t.Fatal(err)
	}
	uctx, ucancel := context.WithTimeout(context.Background(), time.Second)
	defer ucancel()
	if _, err := AccountUsage(uctx, "stuckco", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("usage = %v, want deadline exceeded", err)
	}
	waitFor(t, "the plugin's usage hook was handed a signal", func() bool {
		b, err := os.ReadFile(trace)
		return err == nil && strings.Contains(string(b), `"signal":"live"`)
	})
	waitFor(t, "the plugin's usage hook saw the abort", func() bool {
		b, err := os.ReadFile(trace)
		return err == nil && strings.Contains(string(b), `"aborted":true`)
	})
}

// TestModelListDeadlineTellsTheHook: a models hook whose listWait runs out
// is aborted, not merely raced past — what its vendor read still supports
// stopping stops, instead of one task waiting behind every timed-out read
// (#4678). The hook traces the signal it was handed to a file the test
// reads.
func TestModelListDeadlineTellsTheHook(t *testing.T) {
	sandbox(t)
	stuckWaits(t)
	trace := filepath.Join(t.TempDir(), "trace.jsonl")
	t.Setenv("STUCK_MODELS", "1")
	t.Setenv("STUCK_MODELS_TRACE", "1")
	t.Setenv("STUCK_TRACE", trace)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	abs, err := filepath.Abs("testdata/stuck/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := APIKey(ctx, "stuckco", 0, nil, "k-stuckco", NewAccount); err != nil {
		t.Fatal(err)
	}
	lctx, lcancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer lcancel()
	ps, err := Providers(lctx)
	if err != nil || len(ps) != 1 || ps[0].ID != "stuckco" {
		t.Fatalf("Providers = %+v, %v", ps, err)
	}
	if !ps[0].FellBack {
		t.Fatalf("stuckco listed fellBack=false, want the deadline's fallback")
	}
	waitFor(t, "the models hook was handed a signal", func() bool {
		b, err := os.ReadFile(trace)
		return err == nil && strings.Contains(string(b), `"signal":"live"`)
	})
	waitFor(t, "the deadline aborted the models hook", func() bool {
		b, err := os.ReadFile(trace)
		return err == nil && strings.Contains(string(b), `"aborted":true`)
	})
}

// TestCancelledCallKeepsTheHostTillItsHookEnds: a call magpie gave up on
// still counts as the host's own until its handler ends. A hook that goes
// on after the abort (renewing or saving a sign-in) isn't cut off when the
// host is then stopped and its stdin closes: the host leaves when the hook
// has ended, not at once (#4678).
func TestCancelledCallKeepsTheHostTillItsHookEnds(t *testing.T) {
	sandbox(t)
	trace := filepath.Join(t.TempDir(), "trace.jsonl")
	t.Setenv("STUCK_USAGE", "1")
	t.Setenv("STUCK_USAGE_LINGER", "600")
	t.Setenv("STUCK_TRACE", trace)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	abs, err := filepath.Abs("testdata/stuck/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := APIKey(ctx, "stuckco", 0, nil, "k-stuckco", NewAccount); err != nil {
		t.Fatal(err)
	}
	uctx, ucancel := context.WithTimeout(context.Background(), time.Second)
	defer ucancel()
	if _, err := AccountUsage(uctx, "stuckco", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("usage = %v, want deadline exceeded", err)
	}
	waitFor(t, "the plugin's usage hook saw the abort", func() bool {
		b, err := os.ReadFile(trace)
		return err == nil && strings.Contains(string(b), `"aborted":true`)
	})
	// the host is stopped while the hook is still going: its stdin closes,
	// and it has room to end its calls well past the hook's 600ms
	was := stopWait
	stopWait = 10 * time.Second
	t.Cleanup(func() { stopWait = was })
	Settle()
	b, err := os.ReadFile(trace)
	if err != nil || !strings.Contains(string(b), `"finished":true`) {
		t.Fatalf("the host left before the hook ended; trace: %s (%v)", b, err)
	}
}
