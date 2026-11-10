package agent

import (
	"runtime/debug"
	satomic "sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filememo"
)

// dshIdle is how long the parsed dsh entries and patch lists stay in
// memory after the last look at them. Startup's RenameLegacy filled them
// (~2 MB of YAML nodes with real accounts) and a round that finds nothing
// changed reads none, so they were kept for the life of the process for
// nobody. The window's drift poll and a round that writes parse them again.
const dshIdle = 2 * time.Minute

var dshKept struct {
	last  satomic.Int64
	armed satomic.Bool
	off   bool
}

func init() { dshKept.off = testing.Testing() }

// dshAfter is time.AfterFunc, which a test holds so no check it arms runs
// after it.
var dshAfter = func(d time.Duration, f func()) { time.AfterFunc(d, f) }

// dshUsed notes a look at the parsed entries and arms the idle check.
func dshUsed() {
	if dshKept.off {
		return
	}
	dshKept.last.Store(time.Now().UnixNano())
	if dshKept.armed.CompareAndSwap(false, true) {
		dshAfter(dshIdle, dshIdleCheck)
	}
}

func dshIdleCheck() {
	if left := dshIdle - time.Since(time.Unix(0, dshKept.last.Load())); left > 0 {
		dshAfter(left, dshIdleCheck)
		return
	}
	at := time.Now().UnixNano()
	dropDshParses()
	dshKept.armed.Store(false)
	if dshKept.last.Load() > at && dshKept.armed.CompareAndSwap(false, true) {
		dshAfter(dshIdle, dshIdleCheck) // looked at again meanwhile
	}
	debug.FreeOSMemory()
}

// dropDshParses lets go of every parsed entry and patch list: the next
// look parses the file again.
func dropDshParses() {
	dshNodes.Lock()
	dshNodes.m, dshNodes.size = nil, 0
	dshNodes.Unlock()
	filememo.DropKind("dsh list")
}
