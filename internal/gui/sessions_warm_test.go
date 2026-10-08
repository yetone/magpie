package gui

import (
	"runtime"
	"testing"
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
