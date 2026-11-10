package redact

import (
	"hash/maphash"
	"strings"
	"sync"
)

// Never mask (Options.Allow) holds from the next text on, not for a text
// already sent masked (NicoleS76448271 on X). A conversation sends its
// earlier turns again with each request, and a vendor caches the prompt
// by its bytes: had a value added to Never mask gone as written in those
// turns too, every turn before it would no longer match what the vendor
// cached, and the whole conversation would be read again at full price.
// So a value masked in a text sent on a route is masked there again,
// with the same placeholder, while this magpie remembers that text; a
// new text (the next turn, another conversation) sends it as written.
//
// Keeping a value masked is the safe side of the user's two asks, so a
// hash collision or a text no longer remembered errs as it should: the
// first masks a value the user let go, the second sends it as written
// (as before this memo, and after a restart).
//
// What is remembered is a hash of the route, the text and the value,
// never the value. Only a request for a vendor with a route (Options.sent
// and Route, as sentSpans) counts: a search's query or an embedding's
// input is nothing a vendor caches.
const allowKeptEntries = 1 << 17

type keptKey struct{ text, value uint64 }

var allowKept struct {
	sync.Mutex
	cur, old map[keptKey]struct{}
}

var keptSeed = maphash.MakeSeed()

// keptText is the hash a text s sent on route is remembered by.
func keptText(route, s string) uint64 {
	var h maphash.Hash
	h.SetSeed(keptSeed)
	h.WriteString(route)
	h.WriteByte(0)
	h.WriteString(s)
	return h.Sum64()
}

func keptValue(v string) uint64 {
	return maphash.String(keptSeed, strings.ToLower(strings.TrimSpace(v)))
}

// maskedBefore says v was masked in the text text hashes to, and keeps
// that remembered past the next turnover.
func maskedBefore(text uint64, v string) bool {
	k := keptKey{text, keptValue(v)}
	allowKept.Lock()
	defer allowKept.Unlock()
	if _, ok := allowKept.cur[k]; ok {
		return true
	}
	if _, ok := allowKept.old[k]; ok {
		keptAdd(k)
		return true
	}
	return false
}

// keepMasked remembers v was masked in the text text hashes to.
func keepMasked(text uint64, v string) {
	k := keptKey{text, keptValue(v)}
	allowKept.Lock()
	defer allowKept.Unlock()
	if _, ok := allowKept.cur[k]; !ok {
		keptAdd(k)
	}
}

// keptAdd is called with allowKept held: two generations, as sentAdd.
func keptAdd(k keptKey) {
	if allowKept.cur == nil || len(allowKept.cur) >= allowKeptEntries {
		allowKept.old, allowKept.cur = allowKept.cur, make(map[keptKey]struct{})
	}
	allowKept.cur[k] = struct{}{}
}

func resetKept() {
	allowKept.Lock()
	defer allowKept.Unlock()
	allowKept.cur, allowKept.old = nil, nil
}
