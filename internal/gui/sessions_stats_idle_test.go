package gui

import (
	"net/http/httptest"
	"testing"
)

// Once the Sessions page has gone unasked for statsIdle, every range's
// Stats, the overviews and the answers' tags are let go: a long history's
// were ~15 MB held for good by a window that showed another page, or none.
// The next ask is answered as before, from a new read.
func TestIdleStatsLetGo(t *testing.T) {
	forgetStats()
	t.Cleanup(forgetStats)
	statsIdleTimer.Lock()
	statsIdleTimer.off = false
	statsIdleTimer.Unlock()
	t.Cleanup(func() {
		statsIdleTimer.Lock()
		if statsIdleTimer.t != nil {
			statsIdleTimer.t.Stop()
			statsIdleTimer.t = nil
		}
		statsIdleTimer.off = true
		statsIdleTimer.Unlock()
	})

	before, gen := statsForGen(0)
	statsFor(30)
	writeOverview(httptest.NewRecorder(), 0, "", "", "")
	overviews.Lock()
	had := len(overviews.m)
	overviews.Unlock()
	if had == 0 {
		t.Fatal("the overview asked for was not kept")
	}
	statsIdleTimer.Lock()
	armed := statsIdleTimer.t != nil
	statsIdleTimer.Unlock()
	if !armed {
		t.Fatal("an ask for stats armed no idle timer")
	}

	forgetIdleStats()
	statsMemo.Lock()
	n := len(statsMemo.m)
	statsMemo.Unlock()
	overviews.Lock()
	o := len(overviews.m)
	overviews.Unlock()
	statsTags.Lock()
	tags := len(statsTags.m)
	statsTags.Unlock()
	if n != 0 || o != 0 || tags != 0 {
		t.Fatalf("after the idle timer: %d ranges, %d overviews, %d tags kept; want none", n, o, tags)
	}

	after, gen2 := statsForGen(0)
	if gen2 == gen {
		t.Fatal("the range asked after the idle timer was answered from what it let go")
	}
	if !sameStats(before, after) {
		t.Fatal("the range read again differs from the one let go")
	}
}

// A range being read again behind the page is not let go under it: the
// read would put back a range the memo no longer has.
func TestIdleStatsWaitForABusyRead(t *testing.T) {
	forgetStats()
	t.Cleanup(forgetStats)
	statsFor(0)
	statsMemo.Lock()
	statsMemo.m[0].busy = true
	statsMemo.Unlock()
	t.Cleanup(func() {
		statsMemo.Lock()
		if k := statsMemo.m[0]; k != nil {
			k.busy = false
		}
		statsMemo.Unlock()
	})
	forgetIdleStats()
	statsMemo.Lock()
	kept := statsMemo.m[0] != nil
	statsMemo.Unlock()
	if !kept {
		t.Fatal("a range being read again was let go")
	}
}
