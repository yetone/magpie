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

// The next test's plugins are its own. After Settle, the plugins' list in
// the next test's folder read as changed (checkList), the list the last
// test's folder had being the one seen last: the change was told there,
// in the background, and dropped what that test held.
func TestSettleForgetsTheFoldersPlugins(t *testing.T) {
	t.Setenv("MAGPIE_BUN", "") // no Bun, nor a host: nothing asks the plugins here
	storeSandbox(t)
	t.Cleanup(Settle)
	spec := "opencode-fake-auth"
	writeList(t, `{"plugins":[{"spec":"`+spec+`"}]}`)
	UseCached([]Provider{{ID: "fakeco", Spec: spec, Name: "FakeCo"}})
	Settle()

	storeSandbox(t) // the next test's folder, the same plugin listed in it
	writeList(t, `{"plugins": [{"spec": "`+spec+`"}]}`)
	var told atomic.Int32
	watchChanges(t, func() { told.Add(1) })
	ps := Cached()
	Told()
	if n := told.Load(); n != 0 {
		t.Errorf("the next test's plugins read as changed: %d changes told in it", n)
	}
	if len(ps) != 0 {
		t.Errorf("the next test's plugins answered with the last test's providers: %+v", ps)
	}
}
