package gui

import (
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// The warm-up's garbage goes back to the system when it ends, not over the
// scavenger's next minutes: an idle magpie that just started read ~470 MB.
func TestWarmUpReturnsItsGarbage(t *testing.T) {
	var keep [][]byte
	warmUp(func() {
		// a read that allocates far more than it keeps
		for range 256 {
			keep = append(keep, make([]byte, 1<<20))
		}
		keep = [][]byte{keep[0]}
	})
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if held := m.HeapIdle - m.HeapReleased; held > 32<<20 {
		t.Fatalf("after the warm-up %d MB of freed heap is still held from the system", held>>20)
	}
	if m.HeapAlloc > 128<<20 {
		t.Fatalf("after the warm-up %d MB of heap is still in use", m.HeapAlloc>>20)
	}
	runtime.KeepAlive(keep)
}

// With Codex sessions on this computer the warm-up also reads Codex's
// per-thread effort, which a first Codex page after a start otherwise read
// from the whole ledger while it waited (#2776); without any it doesn't.
// It reads under the tight GC: the usage page's rows are still held then.
func TestWarmUpReadsCodexEffort(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(100))
	statsMemo.Lock()
	saved := statsMemo.m
	statsMemo.Unlock()
	old := warmCodexEffort
	t.Cleanup(func() {
		statsMemo.Lock()
		statsMemo.m = saved
		statsMemo.Unlock()
		warmCodexEffort = old
	})
	for _, c := range []struct {
		keys []string
		want int
	}{{[]string{"claude:a", "codex:b"}, 1}, {[]string{"claude:a"}, 0}} {
		statsMemo.Lock()
		statsMemo.m = map[int]*statsKept{}
		for _, d := range []int{0, 30} {
			statsMemo.m[d] = &statsKept{at: time.Now(), st: sessions.Stats{Keys: c.keys}}
		}
		statsMemo.Unlock()
		read := 0
		warmCodexEffort = func() {
			read++
			if p := gcPercent(); p != tightGCPercent {
				t.Errorf("Codex's effort was read under GOGC=%d, want %d", p, tightGCPercent)
			}
		}
		warmUp(warmSteps()...)
		if read != c.want {
			t.Fatalf("with sessions %v Codex's effort was read %d times, want %d", c.keys, read, c.want)
		}
	}
}

// The warm-up reads every day's and 30 days' stats in one pass over the
// session files, not one pass each (~100 MB of garbage apiece with a real
// history), and keeps both.
func TestWarmUpReadsTheStatsInOnePass(t *testing.T) {
	statsMemo.Lock()
	saved := statsMemo.m
	statsMemo.m = nil
	statsMemo.Unlock()
	old := statsAndNative
	t.Cleanup(func() {
		statsMemo.Lock()
		statsMemo.m = saved
		statsMemo.Unlock()
		statsAndNative = old
	})
	var passes [][]int
	statsAndNative = func(days []int) ([]sessions.Stats, map[string]bool) {
		passes = append(passes, days)
		return make([]sessions.Stats, len(days)), nil
	}
	warmStats(0, 30)
	if len(passes) != 1 || len(passes[0]) != 2 {
		t.Fatalf("the warm-up's stats were read in passes %v, want one for both ranges", passes)
	}
	statsMemo.Lock()
	kept := statsMemo.m[0] != nil && statsMemo.m[30] != nil
	statsMemo.Unlock()
	if !kept {
		t.Fatal("the warm-up's stats are not kept for the page")
	}
	warmStats(0, 30)
	if len(passes) != 1 {
		t.Fatalf("stats kept were read again: passes %v", passes)
	}
}
