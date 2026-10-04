package gateway

// Sinking (01huadalang, a WorkBuddy user with several accounts): an
// account kept answering until it is rate limited, then given every
// request again as soon as its rest ends, is the one a vendor's risk
// control notices. With a provider's or group's Sink on, a key or account
// that answers 429 while it still has quota — a rate limit (failRate), not
// a plan used up — goes to the back of the order its routing gives,
// behind every one not rate limited since, the one that sank first ahead
// of those that sank after it. It comes back to the front only once those
// ahead of it have been rate limited in their turn: the load goes round
// the accounts, where without Sink it goes back to the first each time
// that one's rest ends.
//
// What sank is remembered in memory, until magpie restarts — or until it
// sinks again, which sends it to the back once more. Nothing else changes:
// a resting one still waits behind every one ready (restLast), an account
// out of quota or credit rests as before and doesn't sink, and with Sink
// off the order is the routing's alone.

import (
	"slices"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

type sinking struct {
	at time.Time // when it was rate limited
	n  uint64    // in what order it sank
}

var sunk = struct {
	sync.Mutex
	m map[string]sinking // candidate restKey → when it sank
	n uint64
}{m: map[string]sinking{}}

// sink sends what rests by key to the back of the orders that sink.
func sink(key string, now time.Time) {
	sunk.Lock()
	sunk.n++
	sunk.m[key] = sinking{now, sunk.n}
	sunk.Unlock()
}

// sunkAt is when a candidate sank, if it did.
func sunkAt(c candidate) (sinking, bool) {
	sunk.Lock()
	defer sunk.Unlock()
	s, ok := sunk.m[c.restKey()]
	return s, ok
}

// sinks says whether an order routed so sends the rate limited back:
// in turn goes round already, and a manual group has one member.
func sinks(on bool, routing string) bool {
	return on && routing != provider.Rotate && routing != provider.Manual
}

// sinkOrder is the order of n candidates with those that sank moved
// behind the others, the one that sank first first; the others keep
// theirs.
func sinkOrder(n int, at func(int) candidate) []int {
	idx := make([]int, n)
	seq := make([]uint64, n) // 0: never sank
	for i := range idx {
		idx[i] = i
		if s, ok := sunkAt(at(i)); ok {
			seq[i] = s.n
		}
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		switch sa, sb := seq[a], seq[b]; {
		case sa == sb:
			return 0
		case sa == 0:
			return -1
		case sb == 0:
			return 1
		case sa < sb:
			return -1
		}
		return 1
	})
	return idx
}

// sinkLast is cs with those that sank moved behind the others.
func sinkLast(cs []candidate) []candidate {
	if len(cs) < 2 {
		return cs
	}
	idx := sinkOrder(len(cs), func(i int) candidate { return cs[i] })
	out := make([]candidate, len(cs))
	for i, j := range idx {
		out[i] = cs[j]
	}
	return out
}

// sinkPlanned is sinkLast over a group's planned candidates and what the
// trace says of each, kept together.
func sinkPlanned(out []candidate, order []Weighed) ([]candidate, []Weighed) {
	if len(out) < 2 || len(order) < len(out) {
		return out, order
	}
	idx := sinkOrder(len(out), func(i int) candidate { return out[i] })
	cs, ws := make([]candidate, len(out)), make([]Weighed, len(order))
	copy(ws[len(out):], order[len(out):])
	for i, j := range idx {
		cs[i], ws[i] = out[j], order[j]
		markSunk(&ws[i], out[j])
	}
	return cs, ws
}

// markSunk tells, in the trace, that c sank and when.
func markSunk(w *Weighed, c candidate) {
	if s, ok := sunkAt(c); ok {
		at := s.at
		w.Sunk = &at
	}
}
