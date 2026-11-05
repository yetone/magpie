// Package routes is the gateway's router strategy point (P3 of
// docs/subsystems/plugin-runtime.md): how one pool of routes — a
// provider's accounts or keys, or the heads of a group's members — is
// ordered for a request. The gateway lists the pool, rests, affinity and
// failover itself; a Strategy only says the order.
//
// Smart, the default, is the core's. The others a provider or group can be
// routed by (order, rotate, usage, pace, weight, free, fast, cheap,
// drain, expiring) are here for a plugin to
// register: plugins/rotation does, and off, a pool routed by one of them is
// tried in its listed order, as an unknown routing always was.
package routes

import (
	"log"
	"slices"
	"sync"
	"time"
)

// Seat is one route of a pool, as a strategy weighs it.
type Seat struct {
	// Key is who it is: the account or key's rest key, the same for the
	// same route from request to request
	Key string
	// Account is a subscription's account, not an API key
	Account bool
	// Known: its allowance is known. Used, Soon and Pace are read from it
	Known bool
	// Learns: an account not known yet, of an agent that tells what it has
	// left as it answers (Claude Code's rate_limit_event, Codex's x-codex-*
	// headers)
	Learns bool
	// Used is the share of its allowance used that counts the model, 0–100
	Used float64
	// WeekLow: Used is at LowShare or more for its budget windows alone
	// (a day or longer: the week), no rate cap of it near full
	// (provider.Allowance.CapUsed). Smart and Use before expiry keep it
	// with those that have room: what the week has left is lost at its
	// reset, and kept for last a week at 90% renewing today went unused
	// (ysicing_01858 on Discord).
	WeekLow bool
	// Soon is when its windows renew, the biggest first (Allowance.Renewal)
	Soon []time.Time
	// Lapse is when the soonest of what it has left is lost, apart from
	// its windows' renewals (Allowance.Lapse): zero when not told. Smart
	// goes by it when Soon ties.
	Lapse time.Time
	// Pace is its week left per hour until that renews; an account not
	// known has a whole week (provider.FreshPace), a key none
	Pace float64
	// Weight is its key's weight, at least 1
	Weight int
	// Resting: it rests after a failure now
	Resting bool
	// Spent: it rests because it is out of quota or credit, not for a
	// rate limit or a failure. Drain goes on from it.
	Spent bool
	// Tokens is what magpie sent it lately, decayed by an hour's half-life
	Tokens float64
	// Free: the model this seat would be asked for costs nothing on it —
	// its vendor serves that model at no cost to the subscription's
	// allowance or credits (catalog.Model's Free). FreeFirst goes by it.
	Free bool
	// FreePlan: an account whose plan is a free one (provider.FreePlan:
	// "Free", "Free plan", a trial), as its vendor or plugin names it.
	// FreeExpiring spends it, as it does Free, before a paid one.
	FreePlan bool
	// Credits: a Codex account that goes on past its allowance on its
	// credits, and is let spend them. All but used up, it goes before
	// those used up with nothing to go on with.
	Credits bool
	// First: such an account the user set to spend its credits before
	// another account (provider.CodexCreditsFirst): its windows running
	// out never put it behind the others (seatTier), so it keeps the
	// place an account with room has.
	First bool
	// TTFT is how long it took lately to the first of its reply, in ms,
	// for the model this seat would be asked for: a decayed average of its
	// recent streamed answers, 0 when it hasn't answered one lately.
	// TPS is how fast it wrote them, output tokens a second, 0 when not
	// told (a reply let go in a burst tells none). Fastest goes by them.
	TTFT, TPS float64
	// Left is a prepaid wallet's balance, what it has left in its unit
	// (provider.Allowance.Left, #3950), and LeftIn whose wallet that is:
	// its provider and unit, empty when no balance was told. A wallet has
	// no share and no renewal, so Used and Soon tie; Smart, Pace and
	// LeastUsed then put the one with more left first, only among seats
	// whose LeftIn is the same (byLeft): points are never weighed against
	// another provider's, or another unit.
	Left   float64
	LeftIn string
	// Priced: what its model costs on it now is known. Cost is then what
	// a million tokens in and a million out cost at the request's time —
	// the user's own price, else the list's, at its provider's rate and
	// rate window, doubled in its vendor's peak hours (catalog.Price.In) —
	// and CacheCost a million read from the cache. Its model free there
	// costs 0. Cheapest goes by them.
	Priced          bool
	Cost, CacheCost float64
}

// Strategy orders a pool.
type Strategy struct {
	// Order is the order to try seats in: a permutation of their indexes.
	// pool names the pool (a provider's id, or "group/<id>"), for a
	// strategy that keeps a round per pool. It is asked with two seats or
	// more.
	Order func(pool string, seats []Seat) []int
	// Exact: its order stands as it says. Otherwise, of a provider's API
	// keys, those that fit the request better go first, each kept in its
	// order among those alike.
	Exact bool
	// ByUse: it weighs by Tokens, which the routing trace then shows
	ByUse bool
	// ByFree: it weighs by Free, so the gateway works out which seat's
	// model costs nothing there, and the routing trace then shows it.
	// Only when set: working it out reads each provider's model list.
	ByFree bool
	// ByPlan: it weighs by FreePlan, so the gateway reads each account's
	// plan, and the routing trace then shows a free one as free.
	ByPlan bool
	// BySpeed: it weighs by TTFT and TPS, so the gateway reads each seat's
	// recent speed, and the routing trace then shows it.
	BySpeed bool
	// ByPrice: it weighs by Priced, Cost and CacheCost, so the gateway
	// prices each seat's model at the request's time, and the routing
	// trace then shows it. Only when set: pricing reads each provider's
	// model list and the user's prices.
	ByPrice bool
}

var reg = struct {
	sync.RWMutex
	m map[string][]*Strategy // routing → registered, the last one serving
}{m: map[string][]*Strategy{}}

// Register serves routing by s until remove is called. Registering one
// already served stacks: the last serves until it is removed.
func Register(routing string, s Strategy) (remove func()) {
	e := &s
	reg.Lock()
	reg.m[routing] = append(reg.m[routing], e)
	reg.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			reg.Lock()
			defer reg.Unlock()
			reg.m[routing] = slices.DeleteFunc(slices.Clone(reg.m[routing]), func(x *Strategy) bool { return x == e })
		})
	}
}

// Find is the strategy serving routing: Smart for "", else the last one
// registered; ok is false when none is.
func Find(routing string) (Strategy, bool) {
	if routing == "" {
		return Smart, true
	}
	reg.RLock()
	defer reg.RUnlock()
	l := reg.m[routing]
	if len(l) == 0 {
		return Strategy{}, false
	}
	return *l[len(l)-1], true
}

// Served says whether routing is served now.
func Served(routing string) bool {
	_, ok := Find(routing)
	return ok
}

// Apply is the order s gives seats, checked: an order that isn't a
// permutation of them, or a strategy that panics, leaves them in their
// listed order, logged. A strategy only orders, so failing it fails open:
// every route is still tried.
func Apply(s Strategy, pool string, seats []Seat) (order []int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("routes: %s's strategy panicked: %v", pool, r)
			order = identity(len(seats))
		}
	}()
	order = s.Order(pool, seats)
	seen := make([]bool, len(seats))
	ok := len(order) == len(seats)
	for _, i := range order {
		if !ok || i < 0 || i >= len(seats) || seen[i] {
			ok = false
			break
		}
		seen[i] = true
	}
	if !ok {
		log.Printf("routes: %s's strategy gave %v for %d routes; trying them in order", pool, order, len(seats))
		return identity(len(seats))
	}
	return order
}
