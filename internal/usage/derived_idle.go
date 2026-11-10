package usage

import (
	"runtime/debug"
	"sync/atomic"
	"testing"
	"time"
)

// derivedIdle is how long what is kept of each chunk by its serial (its
// message keys, match hints, first time and heat before a cut) stays
// after the last pairing or heatmap that read it. Each is read again from
// its chunk on the next ask. They were kept by turns, and with no turn
// while nobody looked they stayed for the life of the process: ~4 MB of
// message keys alone in a real history's idle heap.
const derivedIdle = 3 * time.Minute

var derivedKept struct {
	last  atomic.Int64
	armed atomic.Bool
	off   bool
}

func init() { derivedKept.off = testing.Testing() }

// derivedUsed notes a read of the per-chunk caches and arms the idle
// check.
func derivedUsed() {
	if derivedKept.off {
		return
	}
	derivedKept.last.Store(time.Now().UnixNano())
	if derivedKept.armed.CompareAndSwap(false, true) {
		time.AfterFunc(derivedIdle, derivedIdleCheck)
	}
}

// derivedIdleCheck lets the per-chunk caches go once nothing has read
// them for derivedIdle, or looks again when that will be.
func derivedIdleCheck() {
	if derivedKept.off { // a test's timer, after it ended
		derivedKept.armed.Store(false)
		return
	}
	if left := derivedIdle - time.Since(time.Unix(0, derivedKept.last.Load())); left > 0 {
		time.AfterFunc(left, derivedIdleCheck)
		return
	}
	at := time.Now().UnixNano()
	n := dropDerived()
	derivedKept.armed.Store(false)
	if derivedKept.last.Load() > at && derivedKept.armed.CompareAndSwap(false, true) {
		time.AfterFunc(derivedIdle, derivedIdleCheck) // read again meanwhile
	}
	if n > 0 {
		debug.FreeOSMemory()
	}
}

// dropDerived lets go of what is kept of every chunk by its serial and
// says how many were kept.
func dropDerived() int {
	n := 0
	msgKeys.Lock()
	n += len(msgKeys.by)
	msgKeys.by = nil
	msgKeys.Unlock()
	matchHints.Lock()
	n += len(matchHints.by)
	matchHints.by = nil
	matchHints.Unlock()
	firsts.Lock()
	n += len(firsts.by)
	firsts.by = map[uint64]chunkFirst{} // written below an unlock, never nil
	firsts.Unlock()
	heatBefores.Lock()
	n += len(heatBefores.by)
	heatBefores.by = nil
	heatBefores.Unlock()
	return n
}
