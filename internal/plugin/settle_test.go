package plugin

import (
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

// watched, while set, is called with each change told: the change hook
// watchChanges adds calls it.
var (
	watched     atomic.Pointer[func()]
	addWatching sync.Once
)

// watchChanges has f called with each change told (OnChange) until the
// test ends.
func watchChanges(t *testing.T, f func()) {
	addWatching.Do(func() {
		OnChange(func() {
			if f := watched.Load(); f != nil {
				(*f)()
			}
		})
	})
	watched.Store(&f)
	t.Cleanup(func() { watched.Store(nil) })
}

// A test ends with Settle, and Settle returns once the hooks its restart
// told have run. Told in the background, with nothing waiting for it,
// provider.Changed landed in the next test and dropped what that test
// held: its settings were read again while held.
func TestSettleWaitsForTheHooksItTold(t *testing.T) {
	storeSandbox(t)
	Settle() // no host of an earlier test's, nor anything it told, still running
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		watchChanges(t, func() { <-release })
		settled := make(chan struct{})
		go func() {
			Settle()
			close(settled)
		}()
		synctest.Wait() // the hook waits for its release
		select {
		case <-settled:
			t.Error("Settle returned while a hook its restart told was still running: what the hook does lands in the next test")
		default:
		}
		close(release)
		<-settled
	})
}
