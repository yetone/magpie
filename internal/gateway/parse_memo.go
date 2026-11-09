package gateway

import (
	"sync/atomic"
	"unsafe"

	"github.com/yetone/magpie/internal/provider"
)

// bodyFacts is what the readers of a request's conversation that only
// read it take from it: the turn count (turnOf), the folder its agent
// works in and whether that is a git repository, as the text says
// (dirOf, tellWorkPlace), and whether it is the agent compacting its
// conversation (compacting). A body that doesn't parse says none of them.
type bodyFacts struct {
	turn     int
	within   bool
	dir, git string
	compact  bool
}

// factsOf is bodyFacts of body, read in protocol proto. Each of its
// readers parsed the whole conversation, every message and tool output
// decoded into strings of its own, one just after the other: a 2 MB
// conversation was decoded twice over on every request for a number and
// a folder (yg6888 on X: ~1 GB).
//
// For a body being served (holdBody), the facts are kept while the body
// is held, so it is parsed for them once: they are a few words, kept
// whole, where keeping the parse itself would keep a copy of the
// conversation for the request's life.
func factsOf(proto provider.Protocol, body []byte) bodyFacts {
	if len(body) < validMemoMin {
		return readFacts(proto, body)
	}
	key := unsafe.SliceData(body)
	heldBodies.Lock()
	e, ok := heldBodies.facts[key]
	heldBodies.Unlock()
	if ok && e.n == len(body) && e.proto == proto {
		return e.facts
	}
	f := readFacts(proto, body)
	heldBodies.Lock()
	defer heldBodies.Unlock()
	// only while body is held, whole: one released may be freed, and its
	// memory given to another body that would read as this one. A body
	// read in two protocols keeps the last.
	if l := heldBodies.list.Load(); l != nil {
		for _, b := range *l {
			if unsafe.SliceData(b) == key && len(b) == len(body) {
				if heldBodies.facts == nil {
					heldBodies.facts = map[*byte]heldFacts{}
				}
				heldBodies.facts[key] = heldFacts{n: len(body), proto: proto, facts: f}
				break
			}
		}
	}
	return f
}

// readFacts parses body for its bodyFacts.
func readFacts(proto provider.Protocol, body []byte) bodyFacts {
	if f := factsParsed.Load(); f != nil {
		(*f)(body)
	}
	req, err := parseIn(proto, body, true)
	if err != nil {
		return bodyFacts{}
	}
	var f bodyFacts
	f.turn, f.within = turnIn(req)
	f.dir, f.git = workPlace(req)
	f.compact = compacting(req)
	return f
}

// heldFacts is a held body's bodyFacts (factsOf).
type heldFacts struct {
	n     int
	proto provider.Protocol
	facts bodyFacts
}

// factsParsed, when set (tests), is told of each body readFacts parses.
var factsParsed atomic.Pointer[func(body []byte)]
