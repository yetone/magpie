package sessions

import (
	"sync/atomic"
	"testing"
	"time"
)

// Once nothing has read the parses for cacheIdle they are let go, and the
// next read has them back from sessions.json, reading no session file.
// They stay while a save is not on disk yet, or the last one failed.
func TestIdleParsesLetGoAndComeBackFromDisk(t *testing.T) {
	setup(t)
	before := List(0)
	if len(before) == 0 {
		t.Fatal("no sessions listed")
	}
	saved()
	idle := func() { cacheKept.last.Store(time.Now().Add(-2 * cacheIdle).UnixNano()) }

	saving.Lock()
	saving.failed = true
	saving.Unlock()
	idle()
	if dropIdleCache() || !loaded {
		t.Fatal("the parses were let go while the last save had failed")
	}
	saving.Lock()
	saving.failed = false
	saving.Unlock()

	cacheKept.last.Store(time.Now().UnixNano())
	if dropIdleCache() || !loaded {
		t.Fatal("the parses were let go though read just now")
	}

	idle()
	drops := cacheDrops.Load()
	if !dropIdleCache() || loaded || len(cache) != 0 || cacheDrops.Load() != drops+1 {
		t.Fatalf("idle parses kept: loaded %v, %d in memory", loaded, len(cache))
	}

	var parses atomic.Int32
	parseHook = func() { parses.Add(1) }
	t.Cleanup(func() { parseHook = nil })
	after := List(0)
	if n := parses.Load(); n != 0 {
		t.Fatalf("%d session files read again after the let-go; want the parses from disk", n)
	}
	if len(after) != len(before) {
		t.Fatalf("%d sessions after the let-go, %d before", len(after), len(before))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Tokens != before[i].Tokens || after[i].Title != before[i].Title {
			t.Fatalf("session %d after the let-go = %+v, before %+v", i, after[i], before[i])
		}
	}
}
