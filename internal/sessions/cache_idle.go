package sessions

import (
	"runtime/debug"
	"sync/atomic"
	"testing"
	"time"
)

// cacheIdle is how long the parses of every session file stay in memory
// after the last read of them. They are kept on disk too (sessions.json,
// written saveGap after a change), and they were the largest thing magpie
// held while nobody looked: ~14.5 MB of a 58 MB idle heap for a real
// history of 24.6 MB on disk, kept for the life of the process after one
// visit to the Sessions or Usage page. Read again, they come back from
// the file, with no session file read.
const cacheIdle = 3 * time.Minute

var cacheKept struct {
	last  atomic.Int64
	armed atomic.Bool
	off   bool
}

func init() { cacheKept.off = testing.Testing() }

// cacheDrops counts the times the parses were let go, for the tests.
var cacheDrops atomic.Int64

// cacheUsed notes a read of the parses and arms the idle check. mu is held.
func cacheUsed() {
	if cacheKept.off {
		return
	}
	cacheKept.last.Store(time.Now().UnixNano())
	if cacheKept.armed.CompareAndSwap(false, true) {
		time.AfterFunc(cacheIdle, cacheIdleCheck)
	}
}

// cacheIdleCheck lets the parses go once nothing has read them for
// cacheIdle, or looks again when that will be.
func cacheIdleCheck() {
	if left := cacheIdle - time.Since(time.Unix(0, cacheKept.last.Load())); left > 0 {
		time.AfterFunc(left, cacheIdleCheck)
		return
	}
	at := time.Now().UnixNano()
	if !dropIdleCache() {
		time.AfterFunc(cacheIdle, cacheIdleCheck)
		return
	}
	cacheKept.armed.Store(false)
	if cacheKept.last.Load() > at && cacheKept.armed.CompareAndSwap(false, true) {
		time.AfterFunc(cacheIdle, cacheIdleCheck) // read again meanwhile
	}
	debug.FreeOSMemory()
}

// dropIdleCache lets go of the parses in memory when nothing is reading
// or about to change them, once what was parsed since the last save is
// on disk. It says whether it did, or there was nothing to let go.
func dropIdleCache() bool {
	saved() // what the next read loads is what is in memory now
	mu.Lock()
	defer mu.Unlock()
	if !loaded {
		return true
	}
	if time.Since(time.Unix(0, cacheKept.last.Load())) < cacheIdle {
		return false // read while the save was written
	}
	saving.Lock()
	pending := saving.running || saving.next != nil || saving.failed
	saving.Unlock()
	if pending || len(parsing) > 0 || len(overlaid) > 0 || len(behind) > 0 {
		return false
	}
	cache, loaded = map[string]*state{}, false
	cacheGeneration++ // a refresh still holding the old generation drops its parses
	forgetKeptStats()
	forgetKeptList()
	cacheDrops.Add(1)
	return true
}
