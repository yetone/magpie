package usage

import (
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// a key's balance typed by hand is shown less what this counts spent
func init() { provider.KeySpent = KeySpent }

// spendMemo keeps what each API key has spent from a start on, read from
// usage.jsonl once and counted up by Append after: routing asks it on
// every request for a key with a spend cap (provider.SpendCap).
var spendMemo struct {
	sync.Mutex
	path string
	// sums by key and start, each read again once it is spendFresh old:
	// prices change, and the log can be cleared or rewritten
	sums map[spendSlot]spendSum
	// reading counts the reads of a key under way, dirty a call Append
	// wrote for one meanwhile: such a read may have missed it or have it
	// already, so it is not kept
	reading map[string]int
	dirty   map[string]bool
}

type spendSlot struct {
	key   string
	start time.Time
}

type spendSum struct {
	usd float64
	at  time.Time
}

// spendFresh is how long a key's sum read from the log is kept, counted up
// by Append, before it is read again.
const spendFresh = 5 * time.Minute

// spendCounts reports whether r spends from its key: a call magpie sent
// on it from this computer, not one turned away before it went out nor
// one an agent reported.
func spendCounts(r Record) bool {
	return r.ProviderKeyID != "" && !r.IsRejected() && !r.IsReported() && r.Computer == ""
}

// KeySpent is what the calls magpie sent on the API key keyID (its
// provider.KeyID) from start on cost, in USD at the Usage page's prices,
// under whichever provider sent them. A call with no known price counts
// nothing.
func KeySpent(keyID string, start time.Time) float64 {
	path := Path()
	slot := spendSlot{keyID, start.UTC()}
	for try := 0; ; try++ {
		spendMemo.Lock()
		if spendMemo.path != path {
			spendMemo.path, spendMemo.sums = path, nil
		}
		if v, ok := spendMemo.sums[slot]; ok && time.Since(v.at) < spendFresh && !v.at.After(time.Now()) {
			spendMemo.Unlock()
			return v.usd
		}
		if spendMemo.reading == nil {
			spendMemo.reading, spendMemo.dirty = map[string]int{}, map[string]bool{}
		}
		spendMemo.reading[keyID]++
		if spendMemo.reading[keyID] == 1 {
			delete(spendMemo.dirty, keyID)
		}
		spendMemo.Unlock()
		read := time.Now()
		v := readSpent(keyID, start)
		spendMemo.Lock()
		dirty := spendMemo.dirty[keyID]
		if spendMemo.reading[keyID]--; spendMemo.reading[keyID] == 0 {
			delete(spendMemo.reading, keyID)
		}
		// a read Append wrote a call of the key into meanwhile may count it
		// or not: read again, and past three such reads go by what was read
		if !dirty || try >= 2 {
			if !dirty && spendMemo.path == path {
				if spendMemo.sums == nil {
					spendMemo.sums = map[spendSlot]spendSum{}
				}
				// a key's cap counts from one start: one it counted from
				// before (yesterday, a cap since changed) is let go
				for s := range spendMemo.sums {
					if s.key == keyID {
						delete(spendMemo.sums, s)
					}
				}
				spendMemo.sums[slot] = spendSum{v, read}
			}
			spendMemo.Unlock()
			return v
		}
		spendMemo.Unlock()
	}
}

func readSpent(keyID string, start time.Time) float64 {
	cost := NewCoster()
	var spent float64
	Visit(start, func(r Record) {
		if r.ProviderKeyID == keyID && spendCounts(r) && !r.Time.Before(start) {
			c, _ := cost(r)
			spent += c
		}
	})
	return spent
}

// spentNew counts a call Append wrote in the sums of its key kept.
func spentNew(r Record) {
	if !spendCounts(r) {
		return
	}
	spendMemo.Lock()
	defer spendMemo.Unlock()
	if spendMemo.reading[r.ProviderKeyID] > 0 {
		spendMemo.dirty[r.ProviderKeyID] = true
	}
	var cost func(Record) (float64, bool)
	for s, v := range spendMemo.sums {
		if s.key != r.ProviderKeyID || r.Time.Before(s.start) {
			continue
		}
		if cost == nil {
			cost = NewCoster()
		}
		c, _ := cost(r)
		v.usd += c
		spendMemo.sums[s] = v
	}
}
