package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A provider's MaxConcurrency (Discord, Lemon: a Codex account is
// risk-controlled past five or six requests at once) is how many requests
// may be out at the vendor at once on each of its keys or accounts — the
// key, the account, else the provider: a candidate's who(). One more waits
// in a queue of no bound, in the order it came, and is sent when one out
// is done: its reply read to the end, or the agent gone. A request whose
// agent goes away while it waits leaves the queue and is never sent.
//
// Waiting is not failing: a request queued is never passed to a fallback
// for want of a free slot, and the key or account it waits for doesn't
// rest. That is what the setting is
// for — the vendor sees no more than that many — and what is waited is
// told in the route's try (Try.Queued).

// lanes are the slots of each key or account with a limit, and the count
// of those out on one without (limit 0), which take no slot and never wait.
type lanes struct {
	mu sync.Mutex
	m  map[string]*lane
	// freed is closed, and made afresh, each time a slot of any lane is
	// given back or a rest is lifted (signal): a request waiting in one
	// lane looks then for another with room (waitMate)
	freed chan struct{}
}

// signalLocked wakes every request waiting in a lane to look for another
// with room. l.mu is held.
func (l *lanes) signalLocked() {
	if l.freed != nil {
		close(l.freed)
		l.freed = nil
	}
}

// signal is signalLocked, taking l.mu.
func (l *lanes) signal() {
	l.mu.Lock()
	l.signalLocked()
	l.mu.Unlock()
}

// tryTake takes one of who's slots if one is free now and nobody waits
// for it: no limit, or room. false takes none.
func (l *lanes) tryTake(who string, limit int) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string]*lane{}
	}
	ln := l.m[who]
	if ln == nil {
		ln = &lane{}
		l.m[who] = ln
	}
	if limit > 0 && (ln.busy >= limit || len(ln.queue) > 0) {
		l.drop(who, ln)
		return nil, false
	}
	ln.limit = max(limit, 0)
	ln.busy++
	return l.releaser(who, ln), true
}

// lane is one key's or account's: how many are out, and who waits, first
// first.
type lane struct {
	limit int
	busy  int
	queue []chan struct{}
}

// Its own limit (#892) and the queue's bound and wait. A key or account
// may have a limit of its own, over the provider's (provider.LaneLimit),
// and the count is the lane's whatever asks for it: a model, a routing
// group or an agent. A provider's QueueLimit bounds how many wait for each
// of its lanes: one more is turned away at once (errQueueFull), and one
// that waited QueueWait seconds is turned away (errQueueWait) and leaves
// the queue. Turned away, a try is passed on as a failure that rests
// nobody: to the next key, account or member, else told to the agent as a
// 429 in its API's own shape, with Retry-After.
//
// A full one gives way first. Where the request has another key or
// account of the same provider, or another member of its routing group,
// with a slot free now and not resting, that one is asked first and the
// full one keeps its place after it (laneMate): a group spreads its
// requests over members with room before any waits. A fallback provider is
// never asked for want of a slot — it is another vendor, often a paid one
// — and a request whose keys and accounts are all full waits in the queue
// of the one routing chose, but not for it alone: each time a slot of any
// lane is given back (lanes.freed), a rest of one of the others ends or is
// lifted, it looks again, and the first of the others in routing's order
// with a slot free, not resting and not held back as a reserve as its
// provider's accounts stand now (waitMate) takes it, its slot taken for
// it as it leaves the queue (errMoved). 01huadalang on Discord: three
// ZCode accounts at three at once, the first two resting after a 405; once
// they were back, the requests already waiting for the third waited on
// behind its long turns, in every routing, until its own were cut by hand.

// errQueueFull and errQueueWait turn a request away from a lane: its queue
// at its bound, or the request waited as long as it may.
var (
	errQueueFull = errors.New("queue full")
	errQueueWait = errors.New("waited too long")
)

// errMoved is a request that left its lane's queue for another key or
// account with room (waitMate): nobody failed, and it is asked there.
var errMoved = errors.New("gave way to one with room")

// laneRecheck is how often a request waiting in a lane looks again for
// another with room with nothing given back meanwhile: a minute's room
// (MaxRPM) comes back with nothing said.
const laneRecheck = 5 * time.Second

// acquire waits for one of who's limit slots, in turn, with no bound on
// the queue or the wait. It answers the release, to call once the request
// is done with the vendor, and false with no slot taken when ctx ended
// first. A limit of 0 never waits; the request is only counted.
func (l *lanes) acquire(ctx context.Context, who string, limit int) (release func(), ok bool) {
	release, _, err := l.take(ctx, who, limit, 0, 0, nil)
	return release, err == nil
}

// take is acquire with at most queue waiting (0: no bound) for at most
// wait (0: as long as it takes): errQueueFull, errQueueWait, or ctx's
// error when it ended first, with no slot taken. waited is how long it
// stood in the queue: 0 when a slot was free at once or there is no
// limit, so a request that never queued never reads as queued (#2153).
// move, when not nil, is asked while it waits — at once, each time a slot
// of any lane is given back, at the time it names and every laneRecheck —
// whether the request goes to another key or account instead (waitMate,
// which takes that one's slot): true leaves the queue with errMoved.
func (l *lanes) take(ctx context.Context, who string, limit, queue int, wait time.Duration, move func() (bool, time.Time)) (release func(), waited time.Duration, err error) {
	l.mu.Lock()
	if l.m == nil {
		l.m = map[string]*lane{}
	}
	ln := l.m[who]
	if ln == nil {
		ln = &lane{}
		l.m[who] = ln
	}
	if limit <= 0 {
		// the limit was lifted: whoever waits goes now. With none, the
		// request is still counted while it is out, for the pill that
		// says how many run (chentuoyan on Discord)
		ln.limit = 0
		ln.grant()
		ln.busy++
		l.mu.Unlock()
		return l.releaser(who, ln), 0, nil
	}
	ln.limit = limit
	ln.grant() // a limit raised lets more of those waiting go
	if ln.busy < limit && len(ln.queue) == 0 {
		ln.busy++
		l.mu.Unlock()
		return l.releaser(who, ln), 0, nil
	}
	if queue > 0 && len(ln.queue) >= queue {
		l.mu.Unlock()
		return nil, 0, errQueueFull
	}
	ch := make(chan struct{})
	ln.queue = append(ln.queue, ch)
	l.mu.Unlock()
	defer notePhase(ctx, "slot")()
	queued := time.Now()
	var timeout <-chan time.Time
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		timeout = t.C
	}
	err = l.await(ctx, ch, timeout, move)
	if err == nil {
		return l.releaser(who, ln), time.Since(queued), nil
	}
	waited = time.Since(queued)
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, c := range ln.queue {
		if c == ch {
			ln.queue = append(ln.queue[:i], ln.queue[i+1:]...)
			l.drop(who, ln)
			return nil, waited, err
		}
	}
	// granted as it gave up: the slot is given on to the next
	ln.busy--
	ln.grant()
	l.drop(who, ln)
	l.signalLocked()
	return nil, waited, err
}

// await waits for ch, the request's turn in its lane: nil then, else
// ctx's error, errQueueWait at timeout, or errMoved once move says it goes
// elsewhere.
func (l *lanes) await(ctx context.Context, ch chan struct{}, timeout <-chan time.Time, move func() (bool, time.Time)) error {
	for {
		var freed chan struct{}
		var again <-chan time.Time
		var t *time.Timer
		if move != nil {
			l.mu.Lock()
			if l.freed == nil {
				l.freed = make(chan struct{})
			}
			freed = l.freed
			l.mu.Unlock()
			select {
			case <-ch:
				return nil // its turn came first: it goes where it waited
			default:
			}
			ok, next := move()
			if ok {
				return errMoved
			}
			d := laneRecheck
			if !next.IsZero() {
				d = min(d, max(time.Until(next), time.Millisecond))
			}
			t = time.NewTimer(d)
			again = t.C
		}
		select {
		case <-ch:
			if t != nil {
				t.Stop()
			}
			return nil
		case <-ctx.Done():
			if t != nil {
				t.Stop()
			}
			return ctx.Err()
		case <-timeout:
			if t != nil {
				t.Stop()
			}
			return errQueueWait
		case <-freed:
		case <-again:
		}
		if t != nil {
			t.Stop()
		}
	}
}

// free says whether a request for who would be sent at once: no limit, or
// a slot free and nobody waiting for it.
func (l *lanes) free(who string, limit int) bool {
	if limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ln := l.m[who]
	return ln == nil || ln.busy < limit && len(ln.queue) == 0
}

// laneMate is where, after i, the first candidate is with a slot free now
// and room in its minute — another key or account of cands[i]'s provider,
// or in a routing group any member — when cands[i]'s key or account is
// full, or has sent its MaxRPM in the last minute; -1 when it isn't
// full, gave way once already, or none has room. A reserve is never one
// (keepAtOnce): it is held back for its provider not to be used from more
// accounts at once than it lets be, and a full lane is not a failure. Nor
// is one resting: its lane is free because it is resting, and a request
// handed to it for want of a slot would only ask it again before its rest
// is out.
func (s *Server) laneMate(cands []candidate, i int, group bool, gave map[string]bool) int {
	c := cands[i]
	who := c.who()
	if gave[who] || s.ready(c) {
		return -1
	}
	for j := i + 1; j < len(cands); j++ {
		m := cands[j]
		if m.who() == who || !group && m.p.ID != c.p.ID || m.reserve > 0 {
			continue
		}
		if _, resting := restedUntil(m); resting {
			continue
		}
		if s.ready(m) {
			return j
		}
	}
	return -1
}

// restedUntil is when c's rest ends, and whether it rests now: its key's
// or account's, else its model's (restingOf, without reading a key's
// allowance again).
func restedUntil(c candidate) (time.Time, bool) {
	r, ok := restOf(c.restKey())
	if !ok && c.restID() != c.restKey() {
		r, ok = restOf(c.restID())
	}
	return r.Until, ok
}

// waitMate is, for a request waiting in the full lane of cands[i], the
// first other candidate in routing's order that would take it now, with
// its slot taken for it (release): another key or account of cands[i]'s
// provider, or in a routing group any member, not tried yet by this
// request, not one that gave way (gave), not resting, with a slot free and
// room in its minute, and not held back as a reserve as its provider's
// accounts stand now (keepAtOnce over them as the rests are now, so a
// reserve that took a seat while the first rested gives it back once they
// are back). Those after i come first, then those of planned that probeOne
// left out for resting, back now. ok is false when none would; next is
// then when the soonest of those left out only for resting is back (zero
// for none), for the request to look again then. A fallback provider is
// never one, as with laneMate.
func (s *Server) waitMate(cands []candidate, i int, group bool, gave map[string]bool, planned []candidate) (mate candidate, release func(), next time.Time, ok bool) {
	c := cands[i]
	who := c.who()
	seat := func(m candidate) string { return m.who() + "|" + m.model }
	seen := map[string]bool{}
	for _, m := range cands[:i+1] {
		seen[seat(m)] = true
	}
	var pool []candidate
	for _, m := range append(slices.Clone(cands[i+1:]), planned...) {
		if !seen[seat(m)] {
			seen[seat(m)] = true
			pool = append(pool, m)
		}
	}
	// the reserves of each provider of the pool with accounts at once, as
	// its accounts' rests are now
	reserves := map[string]map[string]bool{}
	reserveNow := func(m candidate) bool {
		k := m.p.ID + "|" + m.model
		set, done := reserves[k]
		if !done {
			n := m.p.AtOnce()
			if n > 0 {
				var all []candidate
				for _, o := range append(slices.Clone(cands), planned...) {
					if o.p.ID == m.p.ID && o.model == m.model && !slices.ContainsFunc(all, func(a candidate) bool { return a.who() == o.who() }) {
						all = append(all, o)
					}
				}
				_, held := keepAtOnce(n, all, nil)
				set = map[string]bool{}
				for _, o := range held {
					set[o.who()] = true
				}
			}
			reserves[k] = set
		}
		return set[m.who()]
	}
	for _, m := range pool {
		if m.who() == who || gave[m.who()] || !group && m.p.ID != c.p.ID {
			continue
		}
		if until, resting := restedUntil(m); resting {
			if next.IsZero() || until.Before(next) {
				next = until
			}
			continue
		}
		if reserveNow(m) || !s.rpms.free(m.who(), m.p.RPMLimit()) {
			continue
		}
		if rel, took := s.lanes.tryTake(m.who(), m.p.LaneLimit()); took {
			m.reserve = 0
			return m, rel, time.Time{}, true
		}
	}
	return candidate{}, nil, next, false
}

// movedAhead is cands with m at i, ahead of the one there, which keeps its
// place after it, and m left out where it stood after i.
func movedAhead(cands []candidate, i int, m candidate) []candidate {
	cs := slices.Clone(cands)
	for j := i + 1; j < len(cs); j++ {
		if cs[j].who() == m.who() && cs[j].model == m.model {
			cs = slices.Delete(cs, j, j+1)
			break
		}
	}
	return slices.Insert(cs, i, m)
}

// dropReserves leaves out, of the candidates after i, the reserves of
// provider id (keepAtOnce).
func dropReserves(cands []candidate, i int, id string) []candidate {
	tail := slices.DeleteFunc(slices.Clone(cands[i+1:]), func(m candidate) bool { return m.reserve > 0 && m.p.ID == id })
	return append(slices.Clone(cands[:i+1]), tail...)
}

// ready is whether c's request would go at once: a slot free in its lane,
// and room in its minute (rpm.go).
func (s *Server) ready(c candidate) bool {
	return s.lanes.free(c.who(), c.p.LaneLimit()) && s.rpms.free(c.who(), c.p.RPMLimit())
}

// laneMessage tells the agent why its request was turned away by c's
// queue: full, or waited out.
func laneMessage(c candidate, err error, queued int64) string {
	if e := (*errRPM)(nil); errors.As(err, &e) {
		return fmt.Sprintf("%s: %s; try again then", c.label(), e.Error())
	}
	lim := c.p.LaneLimit()
	if errors.Is(err, errQueueFull) {
		return fmt.Sprintf("%s: %d requests at once is its limit, and its queue is full (%d waiting); try again shortly", c.label(), lim, c.p.QueueLimit)
	}
	return fmt.Sprintf("%s: %d requests at once is its limit, and this one waited %.1fs for a free slot, as long as it may (%ds); try again shortly", c.label(), lim, float64(queued)/1000, c.p.QueueWait)
}

// releaser gives the slot back, once however often it is called.
func (l *lanes) releaser(who string, ln *lane) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			ln.busy--
			ln.grant()
			l.drop(who, ln)
			l.signalLocked()
			l.mu.Unlock()
		})
	}
}

// grant lets those first in the queue go while there is room.
func (ln *lane) grant() {
	for len(ln.queue) > 0 && (ln.limit <= 0 || ln.busy < ln.limit) {
		ln.busy++
		close(ln.queue[0])
		ln.queue = ln.queue[1:]
	}
}

// drop forgets a lane nobody holds or waits on.
func (l *lanes) drop(who string, ln *lane) {
	if ln.busy <= 0 && len(ln.queue) == 0 && l.m[who] == ln {
		delete(l.m, who)
	}
}

// Lane is how a key's or account's requests stand: out at the vendor, and
// waiting their turn.
type Lane struct {
	Busy    int `json:"busy"`
	Waiting int `json:"waiting"`
	Limit   int `json:"limit"`
}

// concurrency tells each key's or account's Lane. It names the accounts,
// so it answers as quotas does: this machine, or one with the key of the
// gateway shared on the local network. Beside them it tells the whole
// gateway's count, limit or none (#2132, for a status bar): requests is
// every agent request in flight, the ones waiting for a slot among them
// (Busy.Requests, a GET like this one never counted), waiting is how many
// of those wait for a slot, and tools the Claude Code turns waiting on
// their caller's tool results, which are not requests. Each is 0 when
// idle, never left out.
// active lists those requests one by one (Active), [] when idle.
func (s *Server) concurrency(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's concurrency is told to another machine only when magpie is shared on the local network and the request carries its API key")
		return
	}
	if refuseHeld(w, r, heldRefused) {
		return
	}
	lanes := s.Lanes()
	waiting := 0
	for _, ln := range lanes {
		waiting += ln.Waiting
	}
	// and those waiting for a slot of their gateway key's (key_slots.go)
	keys := s.KeyLanes()
	for _, ln := range keys {
		waiting += ln.Waiting
	}
	b := s.Busy()
	writeJSON(w, 200, map[string]any{"object": "concurrency", "data": lanes, "requests": b.Requests, "keys": keys, "waiting": waiting, "tools": b.Tools, "active": s.Active()})
}

// Lanes is how each key or account with requests out or waiting stands,
// limit or none (Limit 0), by its who (provider, provider#key,
// provider@account).
func (s *Server) Lanes() map[string]Lane {
	s.lanes.mu.Lock()
	defer s.lanes.mu.Unlock()
	out := map[string]Lane{}
	for who, ln := range s.lanes.m {
		out[who] = Lane{Busy: ln.busy, Waiting: len(ln.queue), Limit: ln.limit}
	}
	return out
}
