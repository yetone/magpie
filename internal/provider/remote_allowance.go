package provider

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A Remote magpie's allowance, for routing (#6708, zzh11223311 on X): a
// group whose members are two Remote magpies, each with a Claude
// subscription signed in there, had every member "not known" (used 0 in
// the routing log), so Smart and Use before expiry went by the tokens
// sent lately and stickiness alone. The other magpie already tells its
// cards (RemoteCardsPath, read here for the Usage page): routing now goes
// by them too, as kept here, never waiting on the network.
//
// A window's json leaves out what routing reads (Span, Model, Aside), so
// a magpie tells another its cards with Routing beside the windows
// (ToldRouting), and the cards read are given them back (withRouting). A
// card from a magpie too old to tell them isn't weighed: a "7 days ·
// Opus" window would count against Sonnet, a five hours as a week.

// ToldRouting is cs, each card with its windows' Routing, for another
// magpie: the gateway's answer of RemoteCardsPath.
func ToldRouting(cs []SubscriptionQuota) []SubscriptionQuota {
	out := make([]SubscriptionQuota, len(cs))
	for i, q := range cs {
		if len(q.Windows) > 0 {
			q.Routing = make([]lastWindow, len(q.Windows))
			for j, w := range q.Windows {
				q.Routing[j] = lastWindow{w.Span, w.Model, w.Aside}
			}
		}
		out[i] = q
	}
	return out
}

// withRouting lays each card's Routing back on its windows, as a remote
// magpie told them, and marks the card one routing may go by. A window
// whose span wasn't known there is named for it here ("7 days").
func withRouting(cs []SubscriptionQuota) []SubscriptionQuota {
	out := make([]SubscriptionQuota, len(cs))
	for i, q := range cs {
		if len(q.Routing) == len(q.Windows) && len(q.Windows) > 0 {
			ws := make([]QuotaWindow, len(q.Windows))
			for j, w := range q.Windows {
				r := q.Routing[j]
				w.Span, w.Model, w.Aside = r.Span, r.Model, r.Aside
				if w.Span == 0 {
					w.Span = windowSpan(w)
				}
				ws[j] = w
			}
			q.Windows, q.routable = ws, true
		}
		q.Routing = nil
		out[i] = q
	}
	return out
}

// remoteRouteReads are the remote magpies whose cards routing has asked
// for in the background, by remoteCardsID: one ask at a time each.
var remoteRouteReads struct {
	sync.Mutex
	m map[string]bool
}

// RemoteAllowance is what the Remote magpie p has left for model, its id
// there ("claude/claude-sonnet-5-5"), by the card of the provider serving
// it there ("claude"; "relay" for "relay/strata/m" when no card of
// relay/strata is told), as last read from p. inner is model as that
// card counts it ("claude-sonnet-5-5"). Of several accounts there, the
// one with the most left for the model: the other magpie's own routing
// sends it there. ok is false when nothing is known.
//
// It never waits. With ask, cards older than remoteCardsFor, or none, are
// asked for in the background (the other magpie answers from what it
// keeps, asking no vendor), and what was read stands meanwhile: routing
// asks only when it weighs p against another, so a request to one Remote
// magpie alone sends it nothing more.
func RemoteAllowance(p Provider, model string, ask bool) (a Allowance, inner string, ok bool) {
	a, inner, _, ok = RemoteSeat(p, model, ask)
	return a, inner, ok
}

// RemoteSeat is RemoteAllowance, with whether the account its allowance
// is of is signed in to a free plan there: a subscription's card whose
// plan FreePlan names free. Free quota by expiry spends it before a paid
// one, as it does an account of this magpie's own (#6708: weighRouted
// asked only Account.Plan, which a Remote magpie has none of).
func RemoteSeat(p Provider, model string, ask bool) (a Allowance, inner string, free, ok bool) {
	if !p.IsRemoteMagpie() || p.Key == "" || p.Anthropic == "" {
		return nil, "", false, false
	}
	id := remoteCardsID(p)
	c := &remoteCardCache
	c.Lock()
	r, had := c.m[id]
	c.Unlock()
	if ask && (!had || time.Since(r.at) >= remoteCardsFor) {
		askRemoteForRouting(p, id)
	}
	if r.held || len(r.cards) == 0 {
		return nil, "", false, false
	}
	now := time.Now()
	for path := model; ; {
		i := strings.LastIndex(path, "/")
		if i <= 0 {
			return nil, "", false, false
		}
		path = path[:i]
		inner = model[len(path)+1:]
		best, bestUsed, bestFree := Allowance(nil), 0.0, false
		for _, q := range r.cards {
			if q.Provider != path || !q.routable || q.Error != "" || q.Held {
				continue
			}
			qa := allowanceOf(q.Windows, now)
			if len(qa) == 0 {
				continue
			}
			if u, _ := qa.For(inner, now); best == nil || u < bestUsed {
				best, bestUsed, bestFree = qa, u, q.Kind == "subscription" && FreePlan(q.Plan)
			}
		}
		if best != nil {
			return best, inner, bestFree, true
		}
	}
}

// askRemoteForRouting reads p's cards in the background, unless that is
// under way.
func askRemoteForRouting(p Provider, id string) {
	rr := &remoteRouteReads
	rr.Lock()
	if rr.m[id] {
		rr.Unlock()
		return
	}
	if rr.m == nil {
		rr.m = map[string]bool{}
	}
	rr.m[id] = true
	rr.Unlock()
	c := &remoteCardCache
	c.Lock()
	gen := c.gen
	c.Unlock()
	goRead(func() {
		defer func() {
			rr.Lock()
			delete(rr.m, id)
			rr.Unlock()
		}()
		asked := time.Now()
		cards, err := askRemote(context.Background(), p, http.MethodGet, RemoteCardsPath, remoteWait)
		keepRemoteCardsOf(id, gen, asked, cards, err)
	})
}
