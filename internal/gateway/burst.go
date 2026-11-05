package gateway

import (
	"math"
	"slices"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/provider"
)

// inFlight counts, for each key or account (candidate.who), the requests
// routed to it that haven't ended. Routing weighs what each has served
// (Tokens) and its allowance, both of which move only once a reply ends,
// and a key's lane counts a request only once its try begins: requests
// that came together were all planned on the same figures and all went to
// the one first (#5242, Moody-Sin and Tericerice: four at once to one of
// three Antigravity accounts, In turn and By pace alike).
var inFlight = struct {
	sync.Mutex
	m map[string]int
}{m: map[string]int{}}

// burstSpread puts first, of those alike to cs[0] under routing (burstAlike),
// the one with the fewest requests in flight, the order kept between those
// with as few, and counts the request on whichever is first then, in one
// hold of the count, so requests planned at the same moment each see the
// ones before them. The count is given back by the func it returns, when
// the request ends. reorder false counts it where it is: a conversation
// kept on its account, a request pinned to one, a new conversation that
// leastHeld spread. sameMember keeps a group's rule's member first.
func burstSpread(routing string, g *provider.Group, sameMember, reorder bool, cs []candidate, pl planned) ([]candidate, planned, func()) {
	if len(cs) == 0 {
		return cs, pl, func() {}
	}
	inFlight.Lock()
	defer inFlight.Unlock()
	if reorder && len(cs) > 1 && len(pl.order) >= len(cs) {
		best := 0
		for i := 1; i < len(cs); i++ {
			if burstAlike(routing, g, sameMember, cs, pl.order, i) && inFlight.m[cs[i].who()] < inFlight.m[cs[best].who()] {
				best = i
			}
		}
		if best > 0 {
			cs = append(append([]candidate{cs[best]}, cs[:best]...), cs[best+1:]...)
			pl.order = append(append([]Weighed{pl.order[best]}, pl.order[:best]...), pl.order[best+1:]...)
			if routingOf(routing) == provider.Rotate {
				for j := range pl.order {
					pl.order[j].Turn = j == 0
				}
			}
		}
	}
	who := cs[0].who()
	inFlight.m[who]++
	var once sync.Once
	return cs, pl, func() {
		once.Do(func() {
			inFlight.Lock()
			if inFlight.m[who]--; inFlight.m[who] <= 0 {
				delete(inFlight.m, who)
			}
			inFlight.Unlock()
		})
	}
}

// burstAlike says whether cs[i] is one routing would as soon send the
// request to as cs[0]: ready as cs[0] is, on the same footing (a fallback
// as a fallback, a key fit for the protocol as well, through the same
// remote magpie), and alike by what its routing goes by — any of a round
// In turn; By pace, both on a pace alike within a tenth with no wallet
// told apart and neither near its cap; Least used, used within a point.
// Other routings pick for their own reasons and keep their order. In a
// group, one of a provider routed its own way is alike only to its own
// provider's, by its provider's routing; a group in the group keeps its
// own order.
func burstAlike(routing string, g *provider.Group, sameMember bool, cs []candidate, order []Weighed, i int) bool {
	a, b := order[0], order[i]
	c0, ci := cs[0], cs[i]
	if a.Rest != nil || a.Aside || a.Reserve > 0 || c0.capped != nil || ci.capped != nil {
		return false
	}
	if b.Rest != nil || b.Aside || b.Reserve > 0 || b.Barred || b.Held || standsIn(order, i) || lastBehind(order, i) || busyBehind(order, i) {
		return false
	}
	if a.Fallback != b.Fallback || a.Fit != b.Fit || a.Speaks != b.Speaks || c0.noURL != ci.noURL || !slices.Equal(a.Via, b.Via) {
		return false
	}
	if sameMember && c0.member != ci.member {
		return false
	}
	if g != nil {
		if strings.HasPrefix(c0.member, provider.GroupPrefix) || strings.HasPrefix(ci.member, provider.GroupPrefix) {
			return false
		}
		if ownRouting(c0.p, *g) || ownRouting(ci.p, *g) {
			if c0.p.ID != ci.p.ID {
				return false
			}
			routing = c0.p.Routing
		}
	}
	switch routingOf(routing) {
	case provider.Rotate:
		return true
	case provider.Pace:
		return a.Learns == b.Learns && burstTier(a) == 0 && burstTier(b) == 0 && paceAlike(a.Pace, b.Pace) && sameBalance(a, b)
	case provider.LeastUsed:
		return a.Known == b.Known && burstTier(a) == burstTier(b) && math.Abs(a.Used-b.Used) < 1 && sameBalance(a, b)
	}
	return false
}

// burstTier is routes.seatTier of a weighed seat: 0 with room, 1 near
// its cap, 2 all but used up; 0 for an account set to spend its credits
// first, and for a week alone that low.
func burstTier(w Weighed) int {
	switch {
	case w.CreditsFirst:
		return 0
	case w.Used >= usedShare:
		return 2
	case w.Used >= lowShare && !w.WeekLow:
		return 1
	}
	return 0
}

// paceAlike says whether two paces are in a band of routes.Pace: within a
// tenth of each other, or neither one's (a key has none).
func paceAlike(a, b float64) bool {
	hi, lo := max(a, b), min(a, b)
	return hi <= 0 || lo >= 0.9*hi
}

// sameBalance says whether two seats told the same of a wallet, or none.
func sameBalance(a, b Weighed) bool {
	if a.Balance == nil || b.Balance == nil {
		return a.Balance == nil && b.Balance == nil
	}
	return *a.Balance == *b.Balance
}
