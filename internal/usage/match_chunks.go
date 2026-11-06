package usage

import (
	"hash/maphash"
	"slices"
	"sync"
	"time"
)

// matchHints are what matchedBlocks needs to know of a local chunk before
// decoding it, kept by the chunk's serial: the request ids its calls name,
// the sessions its calls can be matched in, and its last call's time. A
// poll matched the gateway's calls against every session file of the
// period, decoding each twice to find the few sessions the gateway had
// calls of: ~0.8 GB a minute and seconds of CPU while an agent worked
// (#2278). A chunk none of whose ids or sessions the gateway names can
// match nothing, and is not decoded.
var matchHints struct {
	sync.Mutex
	by   map[uint64]*chunkHints
	turn uint64
}

type chunkHints struct {
	ids      []uint32 // matchHash of each request id, sorted, once each
	sessions []uint64 // matchHash of each session a call can match in
	latest   time.Time
	turn     uint64
}

// hintsKept is how many matchings a chunk's hints outlive the last that
// read them: the page, the Sessions pages and the ledger match chunks of
// their own.
const hintsKept = 8

var matchSeed = maphash.MakeSeed()

func matchHash(s string) uint64 { return maphash.String(matchSeed, s) }

// matchOpening, in tests, is told each local chunk matchedBlocks decodes.
var matchOpening func(*rowChunk)

// hintsOf are chunks' hints, read from a chunk only when none are kept for
// its serial.
func hintsOf(chunks []*rowChunk, rows *chunkRows) []*chunkHints {
	matchHints.Lock()
	matchHints.turn++
	turn := matchHints.turn
	out := make([]*chunkHints, len(chunks))
	for i, c := range chunks {
		if h := matchHints.by[c.serial]; c.serial != 0 && h != nil {
			h.turn = turn
			out[i] = h
		}
	}
	matchHints.Unlock()
	made := map[uint64]*chunkHints{}
	for i, c := range chunks {
		if out[i] != nil {
			continue
		}
		h := &chunkHints{turn: turn}
		open := rows.of(c)
		for j := range open.Rows {
			p := &open.Rows[j]
			if p.Time().After(h.latest) {
				h.latest = p.Time()
			}
			if id := open.Strings[open.text(p, 11)]; id != "" {
				h.ids = append(h.ids, uint32(matchHash(id)))
			}
			if p.Tokens[0]+p.Tokens[1]+p.Tokens[2]+p.Tokens[3] == 0 && p.Flags&16 == 0 {
				continue
			}
			session := open.Strings[open.text(p, 13)]
			if a := open.Strings[open.text(p, 0)]; agyLocal(a) || session == "" && agyGateway(a) {
				session = agyMatchSession
			}
			if s := matchHash(session); len(h.sessions) == 0 || h.sessions[len(h.sessions)-1] != s {
				h.sessions = append(h.sessions, s)
			}
		}
		slices.Sort(h.ids)
		h.ids = slices.Clip(slices.Compact(h.ids))
		slices.Sort(h.sessions)
		h.sessions = slices.Clip(slices.Compact(h.sessions))
		out[i] = h
		if c.serial != 0 {
			made[c.serial] = h
		}
	}
	matchHints.Lock()
	if matchHints.by == nil {
		matchHints.by = map[uint64]*chunkHints{}
	}
	for serial, h := range made {
		matchHints.by[serial] = h
	}
	for serial, h := range matchHints.by {
		if h.turn+hintsKept < matchHints.turn {
			delete(matchHints.by, serial)
		}
	}
	matchHints.Unlock()
	return out
}

// namesAny says some id of ids is in set
func namesAny[T comparable](ids []T, set map[T]struct{}) bool {
	if len(set) == 0 {
		return false
	}
	for _, id := range ids {
		if _, ok := set[id]; ok {
			return true
		}
	}
	return false
}
