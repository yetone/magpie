package gui

import (
	"math"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"testing"
	"time"
)

func gcPercent() int {
	p := debug.SetGCPercent(100)
	debug.SetGCPercent(p)
	return p
}

// The usage page's read of the whole history runs under a tighter GOGC, so
// its heap (every row at once) isn't let grow to twice that, and puts back
// what was set when it ends; a lower GOGC, or off, is kept as it is.
func TestUnderTightGC(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(100))
	for _, before := range []int{100, 200, 10, -1} {
		debug.SetGCPercent(before)
		var during int
		underTightGC(func() { during = gcPercent() })
		want := min(before, tightGCPercent)
		if before < 0 {
			want = before
		}
		if during != want {
			t.Errorf("GOGC=%d: read under %d, want %d", before, during, want)
		}
		if after := gcPercent(); after != before {
			t.Errorf("GOGC=%d: after the read it is %d", before, after)
		}
	}
}

// Under it no memory limit is set: the warm-up's read held at what Go
// held when it began collected every ~160 ms for 13 s of a cold start.
func TestUnderTightGCSetsNoMemoryLimit(t *testing.T) {
	defer debug.SetMemoryLimit(debug.SetMemoryLimit(math.MaxInt64))
	var during int64
	underTightGC(func() { during = debug.SetMemoryLimit(-1) })
	if during != math.MaxInt64 {
		t.Errorf("read under limit %d, want none", during)
	}
}

// Two reads under it at once put back what was set before the first, in
// either order they end: each putting back what it found left the second's
// 25 when the first ended before it.
func TestTightGCOverlappingReads(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(100))
	defer debug.SetMemoryLimit(debug.SetMemoryLimit(math.MaxInt64))
	for _, firstEndsFirst := range []bool{true, false} {
		first := tightenGC()
		second := tightenGC()
		if p := gcPercent(); p != tightGCPercent {
			t.Fatalf("two reads under GOGC=%d, want %d", p, tightGCPercent)
		}
		if firstEndsFirst {
			first()
		} else {
			second()
		}
		if p := gcPercent(); p != tightGCPercent {
			t.Errorf("one read still in flight under GOGC=%d, want %d", p, tightGCPercent)
		}
		first()
		second()
		if p := gcPercent(); p != 100 {
			t.Errorf("first ends first %v: after both GOGC=%d, want 100", firstEndsFirst, p)
		}
		if l := debug.SetMemoryLimit(-1); l != math.MaxInt64 {
			t.Errorf("first ends first %v: after both the limit is %d", firstEndsFirst, l)
		}
	}
}

// The warm-up's usage read is the one under it, and the warm-up's other
// reads are not.
func TestWarmUpTightensOnlyTheUsageRead(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(100))
	var during int
	warmUp(func() { during = gcPercent() })
	if during != 100 {
		t.Fatalf("a warm-up step read under GOGC=%d, want 100", during)
	}
}

// TestTightPercentLeavesRoom: a tight GC on a small heap lets it grow by
// tightGCRoom, not a tenth, which on a first start collected ~28 times a
// second (#2278); a large heap is held to a tenth, and a lower or off
// GOGC is kept.
func TestTightPercentLeavesRoom(t *testing.T) {
	room := tightGCRoom
	tightGCRoom = 32 << 20
	defer func() { tightGCRoom = room }()
	for _, c := range []struct {
		live int64
		was  int
		want int
	}{
		{50 << 20, 100, 64},
		{8 << 20, 100, 100},
		{8 << 20, 300, 300},
		{320 << 20, 100, tightGCPercent},
		{2 << 30, 100, tightGCPercent},
		{50 << 20, 5, 5},
		{50 << 20, -1, -1},
		{0, 100, tightGCPercent},
	} {
		if got := tightPercent(c.live, c.was); got != c.want {
			t.Errorf("tightPercent(%d MB, %d) = %d, want %d", c.live>>20, c.was, got, c.want)
		}
	}
}

// TestTightGCRetunesAfterACollection: while a read is under a tight GC,
// each collection sets GOGC for the heap it left, and once the read ends
// GOGC is what it was before.
func TestTightGCRetunesAfterACollection(t *testing.T) {
	room := tightGCRoom
	tightGCRoom = 1 << 40
	defer func() { tightGCRoom = room }()
	before := debug.SetGCPercent(100)
	defer debug.SetGCPercent(before)
	undo := tightenGC()
	if got := gcPercent(); got != tightGCPercent {
		t.Fatalf("GOGC before a collection = %d, want %d", got, tightGCPercent)
	}
	deadline := time.Now().Add(10 * time.Second)
	for gcPercent() != 100 {
		if time.Now().After(deadline) {
			t.Fatalf("GOGC after collections = %d, want 100 for a heap far under the room", gcPercent())
		}
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
	undo()
	if got := gcPercent(); got != 100 {
		t.Fatalf("GOGC after the read = %d, want 100", got)
	}
}

// TestSteadyPercent: between reads the heap grows by a quarter of what it
// holds, by 16 MB when that is more, and never by more than Go's default.
func TestSteadyPercent(t *testing.T) {
	for _, c := range []struct {
		live int64
		want int
	}{
		{0, 25},
		{60 << 20, 26},
		{64 << 20, 25},
		{8 << 20, 100},
		{16 << 20, 100},
		{32 << 20, 50},
		{500 << 20, 25},
	} {
		if got := steadyPercent(c.live); got != c.want {
			t.Errorf("steadyPercent(%d MB) = %d, want %d", c.live>>20, got, c.want)
		}
	}
}

// steadyOff puts back what steadyGCStart set, for the tests after.
func steadyOff(t *testing.T) {
	before := gcPercent()
	t.Cleanup(func() {
		tightGC.Lock()
		tightGC.steady = false
		tightGC.Unlock()
		debug.SetGCPercent(before)
	})
}

// TestSteadyGCKeepsTheUsersGOGC: a GOGC the user set is what the process
// runs under.
func TestSteadyGCKeepsTheUsersGOGC(t *testing.T) {
	steadyOff(t)
	t.Setenv("GOGC", "200")
	debug.SetGCPercent(200)
	steadyGCStart()
	if got := gcPercent(); got != 200 {
		t.Fatalf("GOGC=200 set by the user, magpie runs under %d", got)
	}
	tightGC.Lock()
	steady := tightGC.steady
	tightGC.Unlock()
	if steady {
		t.Fatal("a GOGC the user set is retuned after each collection")
	}
}

// TestSteadyGCRetunesAfterACollection: from the start GOGC is a quarter,
// each collection sets it for the heap it left (steadyPercent: a whole
// package's test heap is over 16 MB, so not always Go's 100), a read
// under a tight GC still sets its tenth, and after the read the next
// collection sets it for the heap again.
func TestSteadyGCRetunesAfterACollection(t *testing.T) {
	steadyOff(t)
	t.Setenv("GOGC", "")
	debug.SetGCPercent(100)
	steadyGCStart()
	if got := gcPercent(); got != steadyGCPercent {
		t.Fatalf("GOGC before a collection = %d, want %d", got, steadyGCPercent)
	}
	// retuned: GOGC is steadyPercent of the heap the last collection left,
	// which a test's heap of under 64 MB tells from the quarter set before
	retuned := func() {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			runtime.GC()
			time.Sleep(time.Millisecond)
			s := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
			metrics.Read(s)
			live := int64(s[0].Value.Uint64())
			want := steadyPercent(live)
			if want == steadyGCPercent {
				t.Skipf("a live heap of %d MB is retuned to the quarter it starts at", live>>20)
			}
			if got := gcPercent(); got == want {
				return
			} else if time.Now().After(deadline) {
				t.Fatalf("GOGC after collections = %d, want %d for %d MB live", got, want, live>>20)
			}
		}
	}
	retuned()
	debug.SetGCPercent(steadyGCPercent)
	undo := tightenGC()
	if got := gcPercent(); got != tightGCPercent {
		t.Fatalf("a read under a tight GC runs under %d, want %d", got, tightGCPercent)
	}
	runtime.GC()
	runtime.GC()
	if got := gcPercent(); got != tightGCPercent {
		t.Fatalf("a read under a tight GC runs under %d after collections, want %d", got, tightGCPercent)
	}
	undo()
	retuned()
}
