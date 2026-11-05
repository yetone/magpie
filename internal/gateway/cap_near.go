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
// reply told nothing, the usage read again now (at most carefulWait). The
// cap is then passed by one turn at most, not by every turn at once.

import (
	"context"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// nearCapPoints is how many points under a cap an account takes one turn at a
// time: as careful caps read again (carefulNear), a turn of a big model
// on a small plan spending a few points.
const nearCapPoints = carefulNear

// nearCaps is each account's one turn near its cap, by candidate.who().
type nearCaps struct {
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
// cap set on it, by what is known of its usage at now, and the share it
// is at for the model.
func nearCapOf(c candidate, now time.Time) (float64, bool) {
	a := c.p.Account
	if a == nil {
		return 0, false
	}
	caps := provider.HoldCaps(c.p, a.Agent, a.User)
	if a.Agent == "codex" && freeOnChatGPT(c.model, a.Plan) {
		return 0, false // spends nothing a cap keeps (capHeld)
	}
	if !caps.Holds() {
		return 0, false
	}
	al, ok := allowances(a.UsageAgent())[a.User]
	if !ok || !al.NearCap(c.model, caps, nearCapPoints, now) {
		return 0, false
	}
	used, _ := al.For(c.model, now)
	return used, true
}

// nearCapAgain, once a turn has waited for the one before it on an
// account near its cap that was at used then, reads the account's usage
// again when nothing has told it since (the reply before it said nothing
// of it), waiting at most carefulWait.
func nearCapAgain(ctx context.Context, c candidate, used float64) {
	if now, ok := nearCapOf(c, time.Now()); !ok || now != used {
		return // heard from the reply before it, or renewed
	}
	a := c.p.Account
	ctx, cancel := context.WithTimeout(ctx, carefulWait)
	defer cancel()
	readUsageNow(ctx, a.UsageAgent(), a.User)
}
