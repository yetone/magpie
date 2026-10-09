package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

// lanes are the slots of each key or account with a limit.
type lanes struct {
	mu sync.Mutex
	m  map[string]*lane
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
// with a slot free now, that one is asked first and the full one keeps its
// place after it (laneMate): a group spreads its requests over members
// with room before any waits. A fallback provider is never asked for want
// of a slot — it is another vendor, often a paid one — and a request whose
// keys and accounts are all full waits for the one routing chose.

// errQueueFull and errQueueWait turn a request away from a lane: its queue
// at its bound, or the request waited as long as it may.
var (
	errQueueFull = errors.New("queue full")
	errQueueWait = errors.New("waited too long")
)

// acquire waits for one of who's limit slots, in turn, with no bound on
// the queue or the wait. It answers the release, to call once the request
// is done with the vendor, and false with no slot taken when ctx ended
// first. A limit of 0 takes no slot.
func (l *lanes) acquire(ctx context.Context, who string, limit int) (release func(), ok bool) {
	release, err := l.take(ctx, who, limit, 0, 0)
	return release, err == nil
}

// take is acquire with at most queue waiting (0: no bound) for at most
// wait (0: as long as it takes): errQueueFull, errQueueWait, or ctx's
// error when it ended first, with no slot taken.
func (l *lanes) take(ctx context.Context, who string, limit, queue int, wait time.Duration) (release func(), err error) {
	l.mu.Lock()
	if l.m == nil {
		l.m = map[string]*lane{}
	}
	ln := l.m[who]
	if limit <= 0 {
		if ln != nil {
			// the limit was lifted: whoever waits goes now
			ln.limit = 0
			ln.grant()
		}
		l.mu.Unlock()
		return func() {}, nil
	}
	if ln == nil {
		ln = &lane{}
		l.m[who] = ln
	}
	ln.limit = limit
	ln.grant() // a limit raised lets more of those waiting go
	if ln.busy < limit && len(ln.queue) == 0 {
		ln.busy++
		l.mu.Unlock()
		return l.releaser(who, ln), nil
	}
	if queue > 0 && len(ln.queue) >= queue {
		l.mu.Unlock()
		return nil, errQueueFull
	}
	ch := make(chan struct{})
	ln.queue = append(ln.queue, ch)
	l.mu.Unlock()
	var timeout <-chan time.Time
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-ch:
		return l.releaser(who, ln), nil
	case <-ctx.Done():
		err = ctx.Err()
	case <-timeout:
		err = errQueueWait
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, c := range ln.queue {
		if c == ch {
			ln.queue = append(ln.queue[:i], ln.queue[i+1:]...)
			l.drop(who, ln)
			return nil, err
		}
	}
	// granted as it gave up: the slot is given on to the next
	ln.busy--
	ln.grant()
	l.drop(who, ln)
	return nil, err
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
// full, gave way once already, or none has room.
func (s *Server) laneMate(cands []candidate, i int, group bool, gave map[string]bool) int {
	c := cands[i]
	who := c.who()
	if gave[who] || s.ready(c) {
		return -1
	}
	for j := i + 1; j < len(cands); j++ {
		m := cands[j]
		if m.who() == who || !group && m.p.ID != c.p.ID {
			continue
		}
		if s.ready(m) {
			return j
		}
	}
	return -1
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
// gateway shared on the local network.
func (s *Server) concurrency(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's concurrency is told to another machine only when magpie is shared on the local network and the request carries its API key")
		return
	}
	writeJSON(w, 200, map[string]any{"object": "concurrency", "data": s.Lanes()})
}

// Lanes is how each key or account with requests out or waiting under a
// limit stands, by its who (provider, provider#key, provider@account).
func (s *Server) Lanes() map[string]Lane {
	s.lanes.mu.Lock()
	defer s.lanes.mu.Unlock()
	out := map[string]Lane{}
	for who, ln := range s.lanes.m {
		out[who] = Lane{Busy: ln.busy, Waiting: len(ln.queue), Limit: ln.limit}
	}
	return out
}
