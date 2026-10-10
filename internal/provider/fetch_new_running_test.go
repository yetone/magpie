package provider

import (
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// A page reads FetchingNew as soon as its background call returns. Keep
// the fetch blocked, and use one P so that the immediate read also covers
// the interval before its goroutine starts (#1058).
func TestFetchNewBackgroundRegistered(t *testing.T) {
	old := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(old) })
	newFetchHome(t)
	for _, tc := range []struct {
		name  string
		start func(time.Duration)
	}{
		{"Behind", FetchNewBehind},
		{"Soon", FetchNewSoon},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newSoonAt.Store(0)
			release := holdNewFetch(t)
			tc.start(time.Second)
			if !FetchingNew() {
				t.Error("background call returned before its fetch was registered")
			}
			// Let the worker reach the lock: it must not count itself twice.
			runtime.Gosched()
			if n := newRunning.Load(); n != 1 {
				t.Errorf("one background fetch counted %d times", n)
			}
			at := newSoonAt.Load()
			var callers sync.WaitGroup
			for range 20 {
				callers.Go(func() {
					FetchNewBehind(time.Second)
					FetchNewSoon(time.Second)
				})
			}
			callers.Wait()
			if n := newRunning.Load(); n != 1 || newSoonAt.Load() != at {
				t.Errorf("calls while running started more work: count %d", n)
			}
			// A long-running fetch is shared even after Soon's interval.
			newSoonAt.Store(0)
			FetchNewSoon(time.Second)
			if n := newRunning.Load(); n != 1 || newSoonAt.Load() != 0 {
				t.Errorf("Soon started work beside a running fetch: count %d", n)
			}
			newSoonAt.Store(at)
			release()
			waitNewBackground(t)
			if n := newRunning.Load(); n != 0 || FetchingNew() {
				t.Errorf("finished background fetch still counted: %d", n)
			}

			// Soon shares Behind's timestamp; a throttled call counts
			// nothing. Behind still starts at once after the last run.
			release = holdNewFetch(t)
			FetchNewSoon(time.Second)
			if FetchingNew() || fetchingNew.Load() || newSoonAt.Load() != at {
				t.Error("Soon started work within newSoonEvery")
			}
			FetchNewBehind(time.Second)
			if !FetchingNew() {
				t.Error("Behind did not register a fetch within newSoonEvery")
			}
			release()
			waitNewBackground(t)

			// Once the interval has passed, Soon registers new work too.
			newSoonAt.Store(time.Now().Add(-newSoonEvery - time.Second).UnixNano())
			release = holdNewFetch(t)
			FetchNewSoon(time.Second)
			if !FetchingNew() {
				t.Error("Soon did not register a fetch after newSoonEvery")
			}
			release()
			waitNewBackground(t)
			if n := newRunning.Load(); n != 0 {
				t.Errorf("background fetches left a count of %d", n)
			}
		})
	}
}

// Synchronous callers wait for the same lock, but each remains counted
// beside the one background run until its own FetchNew returns.
func TestFetchNewConcurrentCallsCounted(t *testing.T) {
	newFetchHome(t)
	release := holdNewFetch(t)
	var calls sync.WaitGroup
	for range 2 {
		calls.Go(func() { FetchNew(time.Second) })
	}
	for deadline := time.Now().Add(5 * time.Second); newRunning.Load() < 2 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if n := newRunning.Load(); n != 2 || !FetchingNew() {
		t.Errorf("two synchronous calls waiting for the lock: count %d", n)
	}
	FetchNewBehind(time.Second)
	if n := newRunning.Load(); n != 3 {
		t.Errorf("two synchronous and one background call: count %d", n)
	}
	release()
	calls.Wait()
	waitNewBackground(t)
	if n := newRunning.Load(); n != 0 || FetchingNew() {
		t.Errorf("finished calls left a count of %d", n)
	}
}

func newFetchHome(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	forgetAccountCaches()
	oldAt := newSoonAt.Swap(0)
	t.Cleanup(func() {
		newSoonAt.Store(oldAt)
		forgetAccountCaches()
	})
}

// Every held fetch is released and drained before the test's home and
// account readers are restored, even if an assertion ends the test early.
func holdNewFetch(t *testing.T) func() {
	t.Helper()
	newFetches.Lock()
	release := sync.OnceFunc(newFetches.Unlock)
	t.Cleanup(func() {
		release()
		waitNewBackground(t)
	})
	return release
}

func waitNewBackground(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); fetchingNew.Load(); {
		if time.Now().After(deadline) {
			t.Fatal("background fetch did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}
