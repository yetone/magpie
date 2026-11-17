package gateway

// Near its cap, one turn at a time (dyzdyz010 on Discord: capped at 90%,
// used to 100%). Routing holds a capped account by its last reading, and
// turns sent together were each planned on the same one: an account a
// point under its cap took every one of them, and went as far past it as
// they used between them. Within nearCapPoints of a usage cap set on it,
// an account takes one turn at a time. A turn that finds it busy goes to
// the next account or member when there is one, the account kept behind
// them; else it waits for the turn before it. Once its turn comes it is
// held by what is known then: the reading the reply before it left
// (Codex's codex.rate_limits, Claude's rate_limit_event), or, when that
// reply told nothing, the usage read again now (at most carefulWait).
//
// A reply tells the usage as its turn begins, without that turn, and turns
// sent before the account came near its cap may still be out on it, other
// sessions' among them (yalifesign on X: capped at 90%, four or five
// sessions at once, used to 100%). So a turn near the cap also waits for
// every turn out on the account to come back, and reads its usage again
// when one came back since it was last read. The cap is then passed by
// the turns out as the account came near it, then by one turn at most,
// not by every turn at once.

import (
	"context"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// nearChecked, for tests, is told each try's near-cap check, right after
// it, before the try is counted out on the account.
var nearChecked func(near bool)

// nearCapPoints is how many points under a cap an account takes one turn at a
// time: as careful caps read again (carefulNear), a turn of a big model
// on a small plan spending a few points.
const nearCapPoints = carefulNear

// nearCaps is each account's one turn near its cap, by candidate.who().
type nearCaps struct {
	// check makes a turn's near-cap check and its count as out on the
	// account (lanes.plan) one step (#6958)
	check sync.Mutex
	mu    sync.Mutex
	slots map[string]chan struct{}
	wait  int // turns waiting for a slot, for tests
}

func (n *nearCaps) slot(who string) chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.slots == nil {
		n.slots = map[string]chan struct{}{}
	}
	s, ok := n.slots[who]
	if !ok {
		s = make(chan struct{}, 1)
		n.slots[who] = s
	}
	return s
}

// waiting is how many turns wait for an account's turn near its cap.
func (n *nearCaps) waiting() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.wait
}

// tryTake takes who's turn when it is free, and gives back its release.
func (n *nearCaps) tryTake(who string) (func(), bool) {
	s := n.slot(who)
	select {
	case s <- struct{}{}:
		return releaseOnce(s), true
	default:
		return nil, false
	}
}

// take waits for who's turn till ctx ends.
func (n *nearCaps) take(ctx context.Context, who string) (func(), error) {
	s := n.slot(who)
	n.mu.Lock()
	n.wait++
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		n.wait--
		n.mu.Unlock()
	}()
	select {
	case s <- struct{}{}:
		return releaseOnce(s), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func releaseOnce(s chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { <-s }) }
}

// nearCapOf says whether c's account is within nearCapPoints of a usage
// cap set on it, by what is known of its usage at now and what is ahead
// of it: added to its reading, what its turns not in the reading yet will
// spend (turnSpends). It gives the share it is at for the model, and that
// estimate.
func (s *Server) nearCapOf(c candidate, now time.Time) (used, ahead float64, near bool) {
	al, caps, ok := capWatched(c)
	if !ok {
		return 0, 0, false
	}
	used, _ = al.For(c.model, now)
	out, _, turns := s.lanes.out(c.who())
	ahead = s.turnSpend.ahead(c.who(), used, turns, s.lanes.inReading(c.who()), out)
	return used, ahead, al.NearCap(c.model, caps, nearCapPoints+ahead, now)
}

// nearCapOf says whether c's account is within points of a usage cap set
// on it, by what is known of its usage at now, and the share it is at for
// the model.
func nearCapOf(c candidate, now time.Time, points float64) (float64, bool) {
	al, caps, ok := capWatched(c)
	if !ok || !al.NearCap(c.model, caps, points, now) {
		return 0, false
	}
	used, _ := al.For(c.model, now)
	return used, true
}

// capWatched is c's account's allowance as last known, and the caps that
// hold it, when it has any that count the model.
func capWatched(c candidate) (provider.Allowance, provider.WindowCaps, bool) {
	a := c.p.Account
	if a == nil {
		return nil, provider.WindowCaps{}, false
	}
	caps := provider.HoldCaps(c.p, a.Agent, a.User)
	if a.Agent == "codex" && freeOnChatGPT(c.model, a.Plan) {
		return nil, provider.WindowCaps{}, false // spends nothing a cap keeps (capHeld)
	}
	if !caps.Holds() {
		return nil, provider.WindowCaps{}, false
	}
	al, ok := allowances(a.UsageAgent())[a.User]
	return al, caps, ok
}

// turnSpends learns what a turn spends on each account, by candidate.who():
// as its usage moves up, by how much, over the turns that reading has
// that the one before it hadn't. A reply tells the usage as its turn
// begins, so turns out at the vendor, and those come back since the turn
// whose reply told it began, are in no reading yet: what they will spend
// is ahead of the reading.
type turnSpends struct {
	mu sync.Mutex
	m  map[string]turnSpend
}

type turnSpend struct {
	used  float64 // the usage last known
	turns int     // turns come back that reading has
	per   float64 // what a turn spent then, at most maxTurnSpend
}

// maxTurnSpend bounds what one turn is taken to spend: a reading that
// moved by what the vendor's own app spent too is not one turn's.
const maxTurnSpend = 2 * nearCapPoints

// ahead notes who's usage, used, with turns come back and read of them
// in that reading at most (lanes.inReading), and answers what out turns
// at the vendor and those come back that no reading has yet will spend.
// Counting every turn come back as read, the turns of five sessions sent
// together, out as the reading was told, were never ahead of it, and the
// account went past its cap by a round of them (#6958).
func (t *turnSpends) ahead(who string, used float64, turns, read, out int) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]turnSpend{}
	}
	read = min(read, turns)
	s, ok := t.m[who]
	switch {
	case !ok || used < s.used:
		// first known, or renewed: what a turn spent stands
		s.used, s.turns = used, read
	case used > s.used:
		if n := read - s.turns; n > 0 {
			s.per = min((used-s.used)/float64(n), maxTurnSpend)
		}
		s.used, s.turns = used, read
	}
	t.m[who] = s
	return s.per * float64(out+max(turns-s.turns, 0))
}

// userCapped says whether c's account has a usage limit the user set on it
// (a share of its windows, or an amount): not only a Codex account set not
// to spend its credits, which holds it at 100% of a window and sets no
// limit of the user's own. A turn near that alone, with no other account
// or member to go to, is sent and not made to wait for the one before it
// (tmr11235 on X: one account at 95%, no cap, queued).
func userCapped(c candidate) bool {
	a := c.p.Account
	return a != nil && c.p.CapsOf(a.User).Holds()
}

// nearCapAgain reads the usage of c's account, near its cap and at used
// as its turn was planned (ahead of it what turns not read yet spend), again before the turn goes, waiting at most
// carefulWait: when a turn on it came back since it was last read (ended),
// as a reply tells what was used as it begins, without its own turn, and
// what that turn spent is in no reading yet; and when this turn waited for
// the one before it and nothing has told the account's usage since (the
// reply before it said nothing of it).
func nearCapAgain(ctx context.Context, c candidate, used, ahead float64, waited bool, ended time.Time) {
	now, ok := nearCapOf(c, time.Now(), nearCapPoints+ahead)
	if !ok {
		return // renewed
	}
	a := c.p.Account
	at, _, read := usageRead(a.UsageAgent(), a.User)
	spent := !ended.IsZero() && (!read || ended.After(at))
	if !spent && !(waited && now == used) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, carefulWait)
	defer cancel()
	readUsageNow(ctx, a.UsageAgent(), a.User)
}

// phaseQueue is the phase of a request that waits, before any try is begun,
// for the turn of an account near its usage cap: its route says it too
// (Route.Phase), so the Routing page says queued, not routing.
const phaseQueue = "queue"

// showQueued marks ctx's request as waiting for a turn, on its route tr as
// well, and answers the func that takes the mark off.
func (s *Server) showQueued(ctx context.Context, tr *Route) (stop func()) {
	restore := notePhase(ctx, phaseQueue)
	s.trace.update(tr, func(t *Route) {
		if t.Phase == "" {
			t.Phase = phaseQueue
		}
	})
	return func() {
		restore()
		s.trace.update(tr, func(t *Route) {
			if t.Phase == phaseQueue {
				t.Phase = ""
			}
		})
	}
}
