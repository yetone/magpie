package catalog

import (
	"sync"
	"sync/atomic"
)

// pricedMemo keeps what PricedBy and MakerPriced found, for the load of
// Reset count gen. The GUI's provider list prices every model of every
// provider on each poll, and a model looked for by every spelling in
// every maker's catalog cost the poll more than anything else it did
// (most of a provider list's 50ms). What they find depends on the loaded
// catalog, the providers asked and the id, and nothing else, so it is
// kept until the next Reset.
var pricedMemo struct {
	sync.Mutex
	gen int
	by  map[string]pricedFound
}

type pricedFound struct {
	pr Price
	ok bool
}

// pricedMemoEntries bounds the memo: past it, it starts again.
const pricedMemoEntries = 1 << 15

// pricedRuns counts the look-ups pricedOnce did not have (a test reads it).
var pricedRuns atomic.Uint64

// pricedOnce is find's answer for the providers and id under kind, from
// the memo when it has it. find runs without the memo's lock held:
// PricedBy calls itself for a renamed id.
func pricedOnce(at int, kind byte, providers []string, id string, find func() (Price, bool)) (Price, bool) {
	// the key is made on the stack and looked up without a copy (Go reads
	// m[string(b)] in place): made into a string for every price asked,
	// it was ~0.6 MB of a provider list's ~5 MB of garbage an ask
	// (1 KB: a maker price names every maker catalog, past 160 bytes;
	// TestMakerPriceKnownAllocatesNothing holds the bound)
	var buf [1024]byte
	k := append(buf[:0], kind)
	for _, p := range providers {
		k = append(append(k, p...), 0)
	}
	k = append(append(k, 1), id...)
	pricedMemo.Lock()
	if pricedMemo.gen == at && pricedMemo.by != nil {
		if f, ok := pricedMemo.by[string(k)]; ok {
			pricedMemo.Unlock()
			return f.pr, f.ok
		}
	}
	pricedMemo.Unlock()
	key := string(k)
	pricedRuns.Add(1)
	pr, ok := find()
	pricedMemo.Lock()
	if pricedMemo.gen != at || pricedMemo.by == nil || len(pricedMemo.by) >= pricedMemoEntries {
		if pricedMemo.gen <= at {
			pricedMemo.gen, pricedMemo.by = at, map[string]pricedFound{}
		}
	}
	if pricedMemo.gen == at {
		pricedMemo.by[key] = pricedFound{pr, ok}
	}
	pricedMemo.Unlock()
	return pr, ok
}
