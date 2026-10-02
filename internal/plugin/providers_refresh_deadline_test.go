package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// refreshSandbox isolates the test's magpie folders and replaces the
// bounded provider refresh with ask, so a stalled or failing ask is
// proved without Bun, a host or a network. The folders are the test's
// own, as sandbox's are, so nothing under a real HOME is touched.
func refreshSandbox(t *testing.T, ask func(ctx context.Context, epoch uint64) error) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	// a Bun is at hand, as HasBun's check needs to let the refresh start:
	// the replaced ask never runs it
	t.Setenv("MAGPIE_BUN", "bun-not-really")
	// the plugin's providers as this magpie keeps them
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	refreshWriteList(t, refreshAbsPath(t, "testdata/fake/index.js"))
	orig := refreshProviders
	if ask != nil {
		refreshProviders = ask
	}
	// the cache starts stale, as after a sign-in, and the deadlines short
	UseCached([]Provider{{ID: "fakeco", Spec: refreshAbsPath(t, "testdata/fake/index.js"), Name: "FakeCo"}})
	provMu.Lock()
	provGood, provTried = false, false
	provMu.Unlock()
	t.Cleanup(func() {
		Settle()
		refreshProviders = orig
	})
}

func refreshAbsPath(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func refreshWriteList(t *testing.T, spec string) {
	t.Helper()
	b, err := json.Marshal(List{Plugins: []Entry{{Spec: spec}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(listPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func refreshCacheGood() bool {
	provMu.Lock()
	defer provMu.Unlock()
	return provGood
}

// A plugin host that stays alive but never answers the providers call
// must not leave the background refresh waiting for ever: it ends within
// its deadline, keeps the providers already kept, and the refusals are
// bounded rather than endless.
func TestRefreshTimeoutKeepsProviders(t *testing.T) {
	var asks atomic.Int64
	refreshSandbox(t, func(ctx context.Context, _ uint64) error {
		asks.Add(1)
		ctx, cancel := context.WithTimeout(ctx, refreshDeadline)
		defer cancel()
		<-ctx.Done() // the providers RPC never answers
		return ctx.Err()
	})

	// the deadline is short so the test is quick (its value is what is
	// checked, not the default)
	origDeadline, origTries, origBackoff := refreshDeadline, refreshTries, refreshBackoff
	refreshDeadline, refreshTries, refreshBackoff = 150*time.Millisecond, 3, 20*time.Millisecond
	t.Cleanup(func() { refreshDeadline, refreshTries, refreshBackoff = origDeadline, origTries, origBackoff })

	ps := Cached()
	if len(ps) != 1 || ps[0].ID != "fakeco" {
		t.Fatalf("Cached answered %+v, want the providers already kept", ps)
	}

	done := make(chan struct{})
	go func() { Refreshed(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Refreshed never returned: a stalled providers call blocked the refresh")
	}

	if n := asks.Load(); n != 1 {
		t.Fatalf("the stalled providers call was asked %d times, want one bounded stall", n)
	}
	// A timed-out RPC ends this refresh without starting another stalled ask.
	if refreshCacheGood() {
		t.Fatal("the failed refresh marked stale providers current")
	}
	if len(Cached()) != 1 || Cached()[0].ID != "fakeco" {
		t.Fatalf("a failed refresh lost the providers kept: %+v", Cached())
	}
}

// A background refresh that fails is retried with backoff, a bounded
// number of times; one that succeeds on a retry keeps its answer.
func TestRefreshRetriesThenSucceeds(t *testing.T) {
	var asks atomic.Int64
	var stamps []time.Time
	var mu sync.Mutex
	refreshSandbox(t, func(ctx context.Context, epoch uint64) error {
		n := asks.Add(1)
		mu.Lock()
		stamps = append(stamps, time.Now())
		mu.Unlock()
		if n == 1 {
			return errors.New("the provider's vendor is unreachable")
		}
		commitProviders([]Provider{{ID: "fakeco", Spec: refreshAbsPath(t, "testdata/fake/index.js"), Name: "FakeCo", NPM: "fresh"}}, &epoch)
		return nil
	})

	origTries, origBackoff := refreshTries, refreshBackoff
	refreshTries, refreshBackoff = 3, 40*time.Millisecond
	t.Cleanup(func() { refreshTries, refreshBackoff = origTries, origBackoff })

	Cached()
	Refreshed()

	if n := asks.Load(); n != 2 {
		t.Fatalf("the failing refresh was asked %d times, want 2 (one failure, one retry)", n)
	}
	mu.Lock()
	first, second := stamps[0], stamps[1]
	mu.Unlock()
	if gap := second.Sub(first); gap < 30*time.Millisecond {
		t.Fatalf("the retry waited %v, want the backoff of at least 30ms", gap)
	}
	got := Cached()
	if len(got) != 1 || got[0].NPM != "fresh" {
		t.Fatalf("the retry's answer wasn't kept: %+v", got)
	}
}

// Two Cached calls asking at once start one refresh, not two: the check
// for a refresh already in flight and the marking of the cache are one
// step, so neither caller sees the other's half-done state.
func TestRefreshSingleFlight(t *testing.T) {
	var asks atomic.Int64
	release := make(chan struct{})
	refreshSandbox(t, func(ctx context.Context, epoch uint64) error {
		asks.Add(1)
		<-release // held until both callers have asked
		commitProviders([]Provider{{ID: "fakeco", Spec: refreshAbsPath(t, "testdata/fake/index.js"), Name: "FakeCo"}}, &epoch)
		return nil
	})

	const callers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			Cached()
		}()
	}
	close(start)
	// with the refresh held, every caller has answered
	wg.Wait()
	close(release)
	Refreshed()

	if n := asks.Load(); n != 1 {
		t.Fatalf("%d concurrent Cached calls started %d refreshes, want one", callers, n)
	}
}

// Another magpie's invalidation while a refresh is running must not be
// overwritten by that refresh's older completion.
func TestRefreshOutlivedByInvalidation(t *testing.T) {
	stale := make(chan struct{})
	release := make(chan struct{})
	refreshSandbox(t, func(ctx context.Context, epoch uint64) error {
		close(stale)
		<-release
		// as the production ask does: kept only while the invalidation it
		// began under still stands
		commitProviders([]Provider{{ID: "fakeco", Spec: refreshAbsPath(t, "testdata/fake/index.js"), Name: "FakeCo", NPM: "stale"}}, &epoch)
		return nil
	})

	Cached()
	<-stale
	// a sign-in saved meanwhile: the cache is invalidated again, and the
	// newer change's own refresh answers first
	forgetProviders()
	commitProviders([]Provider{{ID: "fakeco", Spec: refreshAbsPath(t, "testdata/fake/index.js"), Name: "FakeCo", NPM: "newer"}}, nil)
	close(release)
	Refreshed()

	got := Cached()
	if len(got) != 1 || got[0].NPM != "newer" {
		t.Fatalf("the older completion went over the newer invalidation: %+v", got)
	}
}

func TestRefreshFailureWaitsForInvalidation(t *testing.T) {
	var asks atomic.Int64
	refreshSandbox(t, func(ctx context.Context, epoch uint64) error {
		if asks.Add(1) <= 3 {
			return errors.New("offline")
		}
		commitProviders([]Provider{{ID: "fakeco", Spec: refreshAbsPath(t, "testdata/fake/index.js"), Name: "Recovered"}}, &epoch)
		return nil
	})
	old := refreshBackoff
	refreshBackoff = time.Millisecond
	t.Cleanup(func() { refreshBackoff = old })
	Cached()
	Refreshed()
	Cached()
	Refreshed()
	if asks.Load() != 3 {
		t.Fatal("Cached retried without an invalidation", asks.Load())
	}
	// A later read cannot restart a failed listing, even when callers keep
	// polling. A changed sign-in or plugin list explicitly enables recovery.
	for i := 0; i < 20; i++ {
		Cached()
	}
	Refreshed()
	if asks.Load() != 3 || refreshCacheGood() {
		t.Fatal("failed listing did not stay stale and idle", asks.Load())
	}
	forgetProviders()
	Cached()
	Refreshed()
	if asks.Load() != 4 || !refreshCacheGood() || Cached()[0].Name != "Recovered" {
		t.Fatal("stale cache did not recover", asks.Load(), Cached())
	}
}

func TestRefreshFailureDuringInvalidationCanRecover(t *testing.T) {
	var asks atomic.Int64
	started, release := make(chan struct{}), make(chan struct{})
	refreshSandbox(t, func(ctx context.Context, epoch uint64) error {
		if asks.Add(1) == 1 {
			close(started)
			<-release
			return errors.New("old listing failed")
		}
		commitProviders([]Provider{{ID: "fakeco", Spec: refreshAbsPath(t, "testdata/fake/index.js"), Name: "Recovered"}}, &epoch)
		return nil
	})
	Cached()
	<-started
	forgetProviders()
	close(release)
	Refreshed()
	if asks.Load() != 2 || Cached()[0].Name != "Recovered" {
		t.Fatal("older failure swallowed the newer invalidation", asks.Load(), Cached())
	}
}

// Use the real Call/get/startOn path: replacing refreshProviders cannot prove
// that a providers deadline leaves the host's initialization budget intact.
func realRefreshPlugin(t *testing.T, initMillis, callMillis int) {
	t.Helper()
	sandbox(t)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	spec := filepath.Join(t.TempDir(), "plugin.mjs")
	js := fmt.Sprintf(`export default async () => {
  await new Promise(r => setTimeout(r, %d));
  return {
   config(c) { c.provider ??= {}; c.provider.slow = { name: "Slow", models: { "slow-1": { name: "Slow One" } } }; },
   auth: { provider: "slow", methods: [{ type: "api", label: "API key" }] },
   provider: { id: "slow", models: async p => { await new Promise(r => setTimeout(r, %d)); return p.models; } }
  };
 };`, initMillis, callMillis)
	if err := os.WriteFile(spec, []byte(js), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(settings.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	refreshWriteList(t, spec)
	if err := os.WriteFile(AuthPath(), []byte(`{"slow":{"type":"api","key":"fake"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	UseCached(nil)
}

func TestRefreshDeadlineLeavesHostStartupBudget(t *testing.T) {
	realRefreshPlugin(t, 1500, 0)
	old := refreshDeadline
	refreshDeadline = 500 * time.Millisecond
	t.Cleanup(func() { refreshDeadline = old })
	Cached()
	Refreshed()
	got := Cached()
	if !Running() || !refreshCacheGood() || len(got) != 1 || got[0].ID != "slow" {
		t.Fatalf("providers deadline cut short host startup: %+v, running=%v", got, Running())
	}
}

func TestRefreshDeadlineBoundsRealProviderCall(t *testing.T) {
	realRefreshPlugin(t, 0, 1500)
	if _, err := Plugins(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := refreshDeadline
	refreshDeadline = 100 * time.Millisecond
	t.Cleanup(func() { refreshDeadline = old })
	Cached()
	done := make(chan struct{})
	go func() { Refreshed(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("real providers RPC outlived its deadline")
	}
	if refreshCacheGood() || !Running() {
		t.Fatal("RPC timeout published a result or killed the initialized host")
	}
}

func TestRefreshInvalidationCancelsStaleCall(t *testing.T) {
	var asks atomic.Int64
	started := make(chan struct{})
	refreshSandbox(t, func(ctx context.Context, epoch uint64) error {
		if asks.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}
		commitProviders([]Provider{{ID: "fakeco", Spec: refreshAbsPath(t, "testdata/fake/index.js"), Name: "Recovered"}}, &epoch)
		return nil
	})
	Cached()
	<-started
	forgetProviders()
	Cached()
	done := make(chan struct{})
	go func() { Refreshed(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stale listing was not cancelled and followed by the new state")
	}
	if asks.Load() != 2 || !refreshCacheGood() || Cached()[0].Name != "Recovered" {
		t.Fatal(asks.Load(), Cached())
	}
}
