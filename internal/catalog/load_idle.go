package catalog

import (
	"runtime/debug"
	"sync/atomic"
	"testing"
	"time"
)

// loadIdle is how long the loaded catalog stays in memory after the last
// read of it. models.dev's api.json is ~6 MB, and parsed, with what is
// voted and indexed of it, it was the largest thing an idle magpie held
// (~7 MB of a ~40 MB heap) for the life of the process. Read again, it
// is parsed again from the same file (~0.1s), as after a Reset.
const loadIdle = 2 * time.Minute

var loadKept struct {
	last  atomic.Int64
	armed atomic.Bool
	off   bool
}

func init() { loadKept.off = testing.Testing() }

// loadDrops counts the times the loaded catalog was let go, for the tests.
var loadDrops atomic.Int64

// loadUsed notes a read of the loaded catalog and arms the idle check.
func loadUsed() {
	if loadKept.off {
		return
	}
	loadKept.last.Store(time.Now().UnixNano())
	if loadKept.armed.CompareAndSwap(false, true) {
		time.AfterFunc(loadIdle, loadIdleCheck)
	}
}

// loadIdleCheck lets the loaded catalog go once nothing has read it for
// loadIdle, or looks again when that will be.
func loadIdleCheck() {
	if left := loadIdle - time.Since(time.Unix(0, loadKept.last.Load())); left > 0 {
		time.AfterFunc(left, loadIdleCheck)
		return
	}
	at := time.Now().UnixNano()
	dropped := dropIdleLoad()
	loadKept.armed.Store(false)
	if loadKept.last.Load() > at && loadKept.armed.CompareAndSwap(false, true) {
		time.AfterFunc(loadIdle, loadIdleCheck) // read again meanwhile
	}
	if dropped {
		debug.FreeOSMemory()
	}
}

// dropIdleLoad lets go of the loaded catalog and what was built of it,
// as Reset does, when nothing has read it for loadIdle. What the
// provider package keeps of it is left alone (Forget is not told): the
// next load reads the same file, and a changed one is Reset by whoever
// wrote it. It says whether there was anything to let go.
func dropIdleLoad() bool {
	loadMu.Lock()
	if !loaded || time.Since(time.Unix(0, loadKept.last.Load())) < loadIdle {
		loadMu.Unlock()
		return false
	}
	loaded = false
	built = nil
	gen++
	mdev, images, windows, outputs, efforts, thinks, names, listed, canonical, labs, normal = nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil
	loadMu.Unlock()
	// what was indexed of the old load is made again for the next
	priceIndexes.Lock()
	priceIndexes.by = nil
	priceIndexes.Unlock()
	makerOwners.Lock()
	makerOwners.by = nil
	makerOwners.Unlock()
	levelIndexes.Lock()
	levelIndexes.by = nil
	levelIndexes.Unlock()
	endpoints.Lock()
	endpoints.by = nil
	endpoints.Unlock()
	pricedMemo.Lock()
	pricedMemo.by = nil
	pricedMemo.Unlock()
	loadDrops.Add(1)
	return true
}
