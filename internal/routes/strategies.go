package routes

import (
	"cmp"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Shares of an allowance past which an account is kept for when the others
// can't take a request: low (in Smart and Use before expiry, of a rate cap:
// Seat.WeekLow), and all but used up — where the agent signed
// in to it is signed in to another, too (provider.SpentShare).
const (
	LowShare  = 90
	UsedShare = provider.SpentShare
)

// tier is 0 for a share with room, 1 for low, 2 for all but used up.
func tier(share float64) int {
	switch {
	case share >= UsedShare:
		return 2
	case share >= LowShare:
		return 1
	}
	return 0
}

// seatTier is tier of the seat's share used, but 0 for one set to spend
// its credits before another account (Seat.First): a window it uses up
// doesn't send its requests elsewhere (jojonas_xyz on X). Running low on
// its week alone (Seat.WeekLow) is 0 too: Smart and Use before expiry
// spend what is lost at the reset, and a week at 90% renewing today was
// kept for last behind one renewing tomorrow (ysicing_01858 on Discord).
// All but used up is 2 whatever the window.
func seatTier(s Seat) int {
	if s.First {
		return 0
	}
	if t := tier(s.Used); t != 1 || !s.WeekLow {
		return t
	}
	return 0
}

// byLeft is how a pool's wallets go by what they have left (Seat.Left),
// as a comparison of two seats by their index: of seats whose balance is
// in one provider's one unit (Seat.LeftIn), the one with more left first;
// a seat that told none after those that did. Points and another
// vendor's points don't compare, so each kind is ordered only within the
// places its own seats take: those places are the told seats' order by
// before (nil: the pool's own order), each kind's seats are put in its
// places by what they have left, and seats of two kinds keep before's
// order between them. Before, a pool with two kinds was weighed by
// nothing (nil), and a group of several providers' accounts spent the
// small balances first (#4095, mycf_711). Nil when no seat told one.
func byLeft(seats []Seat, before func(a, b Seat) bool) func(i, j int) int {
	var told []int
	for i, s := range seats {
		if s.LeftIn != "" {
			told = append(told, i)
		}
	}
	if len(told) == 0 {
		return nil
	}
	if before != nil {
		sort.SliceStable(told, func(a, b int) bool { return before(seats[told[a]], seats[told[b]]) })
	}
	place := make([]int, len(seats))
	for i := range place {
		place[i] = len(seats) // told none: after every one that did
	}
	var kinds []string
	places := map[string][]int{}
	for p, i := range told {
		in := seats[i].LeftIn
		if _, ok := places[in]; !ok {
			kinds = append(kinds, in)
		}
		places[in] = append(places[in], p)
	}
	for _, in := range kinds {
		ps := places[in]
		ids := make([]int, len(ps))
		for k, p := range ps {
			ids[k] = told[p]
		}
		sort.SliceStable(ids, func(a, b int) bool { return seats[ids[a]].Left > seats[ids[b]].Left })
		for k, p := range ps {
			place[ids[k]] = p
		}
	}
	return func(i, j int) int { return cmp.Compare(place[i], place[j]) }
}

// fewerTokens is what Pace and LeastUsed go by after a wallet's balance:
// what magpie sent a seat lately, the least first. It sets the places
// wallets of two kinds keep between them (byLeft).
func fewerTokens(a, b Seat) bool { return a.Tokens < b.Tokens }

func identity(n int) []int {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	return idx
}

// Smart is the default: of those with quota to spare, the one whose
// allowance renews soonest, since what it has left is lost then, while one
// renewing later keeps: the biggest window decides — the week, not the five
// hours in it — and the next one only when that renews in the same hour. A
// biggest window whose left the next can no longer let through before it
// renews (Seat.Unreached: a Volcengine Agent Plan's month two weeks out,
// with more left than two weeks hold) is passed over: what the week
// leaves is lost at its reset then, so the week decides (#5639). An
// account with no week (Claude Enterprise's five hours alone) goes by its
// five hours, so ahead of every week but one renewing sooner; a window not
// started renews its whole span from now (#576). A Codex account that
// spends a reset about to run out by itself (auto-use on, its windows used)
// renews when that is spent, if sooner: what it has left is lost there just
// the same (#718). Only holding one, or one that runs out after the week
// renews, changes nothing. Renewing in the same hours, the one whose
// credits left lapse first (Seat.Lapse) goes first: WorkBuddy's free
// accounts all renew at the month's end, but the credits each was given
// end on their own days (zhaocai_83020 on Discord). Then, of prepaid
// wallets (Seat.Left), the one with more left (#3950). Those alike stay in their order, keeping the
// vendor's prompt cache warm. Past that, whichever has the most left, and
// one all but used up only when nothing else can take it, one that goes
// on on its credits first of those (spentBefore). Only the windows
// that count the model do: Opus's own weekly allowance being used up leaves
// Sonnet alone.
//
// One not known that tells what it has left as it answers goes first,
// once: else, kept after those known, it would never answer and never be
// known (Anthropic's usage endpoint can turn magpie away for hours).
var Smart = Strategy{Order: func(_ string, seats []Seat) []int {
	left := byLeft(seats, nil)
	var fine, low, spent []int
	for i, s := range seats {
		switch seatTier(s) {
		case 2:
			spent = append(spent, i)
		case 1:
			low = append(low, i)
		default:
			fine = append(fine, i)
		}
	}
	sort.SliceStable(fine, func(i, j int) bool {
		a, b := seats[fine[i]], seats[fine[j]]
		if a.Learns != b.Learns {
			return a.Learns
		}
		// a window that can't be used up before it renews any more ranks
		// it no more: the next one does (Seat.Unreached)
		ri, rj := a.Soon[min(a.Unreached, len(a.Soon)):], b.Soon[min(b.Unreached, len(b.Soon)):]
		for k := 0; k < len(ri) || k < len(rj); k++ {
			var x, y time.Time // to the hour, so a few minutes don't reorder
			if k < len(ri) {
				x = ri[k].Truncate(time.Hour)
			}
			if k < len(rj) {
				y = rj[k].Truncate(time.Hour)
			}
			switch {
			case x.Equal(y):
				continue
			case x.IsZero() || y.IsZero(): // not known: after those known
				return y.IsZero()
			}
			return x.Before(y)
		}
		// renewing in the same hours: the one whose credits left lapse
		// first, to the hour; one that tells none after those that do
		x, y := a.Lapse.Truncate(time.Hour), b.Lapse.Truncate(time.Hour)
		if x.Equal(y) {
			// then a wallet with more left (#3950)
			return left != nil && left(fine[i], fine[j]) < 0
		}
		if x.IsZero() || y.IsZero() {
			return y.IsZero()
		}
		return x.Before(y)
	})
	sort.SliceStable(low, func(i, j int) bool { return seats[low[i]].Used < seats[low[j]].Used })
	sort.SliceStable(spent, func(i, j int) bool { return spentBefore(seats[spent[i]], seats[spent[j]]) })
	return leadFirst(seats, append(append(fine, low...), spent...))
}}

// leadFirst is order with the seats of a provider set to be used first
// (Seat.Lead) ahead of the rest, each side in order's own order: a key's
// monthly credit spent before a subscription that serves the same model
// (hzh_1119 on Discord). One all but used up stays with the others that
// are, and one resting is passed over by the gateway as any is.
func leadFirst(seats []Seat, order []int) []int {
	var lead, rest []int
	for _, i := range order {
		if seats[i].Lead && seatTier(seats[i]) < 2 {
			lead = append(lead, i)
		} else {
			rest = append(rest, i)
		}
	}
	if len(lead) == 0 {
		return order
	}
	return append(lead, rest...)
}

// spentBefore orders two seats all but used up: one that goes on on its
// credits first (a Codex account holding them answers past its allowance;
// one with nothing left is refused), then the one with the most left.
func spentBefore(a, b Seat) bool {
	if a.Credits != b.Credits {
		return a.Credits
	}
	return a.Used < b.Used
}

// Ordered tries a pool in its order: the first until it can't answer.
var Ordered = Strategy{Exact: true, Order: func(_ string, seats []Seat) []int { return identity(len(seats)) }}

var drains = struct {
	sync.Mutex
	m map[string]drainAt // pool → the seat it is on
}{m: map[string]drainAt{}}

// drainAt is the seat a pool routed Until used up is on: its Key, and past
// once it was refused for its quota or credit (Drained), so the pool goes
// on from it even if it renews before the pool's next request.
type drainAt struct {
	key  string
	past bool
}

// Drained says key was refused for its quota or its credit: every pool
// routed Until used up that is on it goes on to the next at its next
// request, though key renews before then. The gateway's failover has
// already tried the next for the request refused; without this, a pool
// asked again only after key renewed went back to it.
func Drained(key string) {
	drains.Lock()
	defer drains.Unlock()
	for pool, at := range drains.m {
		if at.key == key {
			drains.m[pool] = drainAt{key, true}
		}
	}
}

// Drain stays on one seat until it is used up, then goes on to the next
// in the listed order, and stays on that one (dzxx0496 on Discord: free
// accounts one after another, the first not taking the requests back when
// its quota renews). The one it is on goes first, the others after it in
// their listed order from there, so failover goes on as Ordered's does.
// Used up is a window of it used up (provider.SpentShareOf: 100, as In
// order goes on at, #530), resting out of quota or credit (Seat.Spent),
// or refused so since the pool's last request (Drained); a rate limit or
// a failure only rests a seat, which the gateway tries last meanwhile,
// and the pool stays on it. With every seat used up it stays where it
// is. A seat it is on that leaves the pool (turned off, removed) leaves
// it on the first not used up, in the listed order. Asking again with
// nothing changed gives the same order, so a preview moves nothing.
var Drain = Strategy{Exact: true, Order: func(pool string, seats []Seat) []int {
	spent := func(s Seat) bool {
		return s.Spent || !s.First && s.Known && s.Used >= provider.SpentShareOf(provider.Drain)
	}
	drains.Lock()
	defer drains.Unlock()
	on := drains.m[pool]
	at := slices.IndexFunc(seats, func(s Seat) bool { return s.Key != "" && s.Key == on.key })
	if at < 0 {
		at = max(0, slices.IndexFunc(seats, func(s Seat) bool { return !spent(s) }))
	} else if on.past || spent(seats[at]) {
		for k := 1; k < len(seats); k++ {
			if i := (at + k) % len(seats); !spent(seats[i]) {
				at = i
				break
			}
		}
	}
	drains.m[pool] = drainAt{key: seats[at].Key}
	return append(identity(len(seats))[at:], identity(at)...)
}}

var turns = struct {
	sync.Mutex
	m map[string]int // pool → requests routed in turn
}{m: map[string]int{}}

// Rotate starts each request one further along the pool.
var Rotate = Strategy{Order: func(pool string, seats []Seat) []int {
	turns.Lock()
	n := turns.m[pool] % len(seats)
	turns.m[pool]++
	turns.Unlock()
	return append(identity(len(seats))[n:], identity(n)...)
}}

// LeastUsed puts a subscription, or a key that reads its own windows, by the
// share of its allowance used, as the vendor says; then, and for other
// keys, by what magpie sent it lately; prepaid wallets, which have no
// share, by what they have left first (Seat.Left). Of those all but used up, one that
// goes on on its credits goes first (spentBefore). A seat that tells no
// share — a pay-as-you-go key, a wallet, an account whose use wasn't read
// — has Used read 0, and by that 0 alone it went first, burning paid
// balance while subscription quota sat idle (#4535): it goes after those
// whose share was told and has room, and before those running low or
// spent, as Smart and Pace keep the unknown. One not known that tells
// what it has left as it answers goes first, once, as in Smart and Pace.
var LeastUsed = Strategy{ByUse: true, Order: func(_ string, seats []Seat) []int {
	left := byLeft(seats, fewerTokens)
	// shared: the seat's share of its allowance was told — a use past 0
	// read from it, or a window it renews by. Without one, Used reads 0
	// because it isn't known, not because there is room.
	shared := func(s Seat) bool {
		if !s.Known {
			return false
		}
		if s.Used > 0 {
			return true
		}
		for _, at := range s.Soon {
			if !at.IsZero() {
				return true
			}
		}
		return false
	}
	// where the seat lands: a learning account first, once, as in Smart
	// and Pace; then those with room whose share was told; then the
	// shareless, kept for their cost before a share nearly gone; then
	// those running low, then spent
	place := func(s Seat) int {
		switch t := tier(s.Used); {
		case s.Learns:
			return 0
		case t == 0 && shared(s):
			return 1
		case t == 0:
			return 2
		default:
			return t + 2
		}
	}
	idx := identity(len(seats))
	sort.SliceStable(idx, func(a, b int) bool {
		sa, sb := seats[idx[a]], seats[idx[b]]
		if pa, pb := place(sa), place(sb); pa != pb {
			return pa < pb
		}
		if ta, tb := tier(sa.Used), tier(sb.Used); ta == 2 && tb == 2 && sa.Credits != sb.Credits {
			return sa.Credits
		}
		if sa.Used != sb.Used {
			return sa.Used < sb.Used
		}
		if left != nil {
			if c := left(idx[a], idx[b]); c != 0 {
				return c < 0
			}
		}
		return sa.Tokens < sb.Tokens
	})
	return idx
}}

// Pace puts a subscription by what it has left of its week per hour until
// that renews, the most first: it has the most to lose at the reset, where
// the most left alone (least used) sends a fresh account ahead of one with
// 80% left and an hour to go, and the soonest reset alone (smart) sends one
// with 3% left and an hour to go ahead of one with 90% and two. The tiers
// are Smart's: one at 90% or more of a rate cap that counts the model —
// the five hours, which it would be sent into before its allowance was
// read again — waits until the others can't answer (a week at 90% alone
// doesn't: what it has left is lost at its reset, Seat.WeekLow), one all
// but used up
// until nothing else can; each tier by the share used, one all but used up
// that goes on on its credits first (spentBefore). Among the rest, one
// not known that tells what it has left as it answers goes first while it
// isn't, as in Smart, else it would never be known; then the pace, those
// alike within a tenth by what a wallet has left (Seat.Left), then by what
// magpie sent them lately, then in their
// order, keeping the vendor's prompt cache warm. An account not known
// counts as a whole week ahead of it; a key has no week, unless it reads
// its own windows (a sub2api key's limits). A Codex account that spends a
// reset about to run out by itself goes by the hours until then when that
// is sooner (Allowance.Pace, #717).
var Pace = Strategy{ByUse: true, Order: func(_ string, seats []Seat) []int {
	// the bands of pace alike within a tenth, the highest first: drawn
	// before sorting, from each band's highest down, so that sorting by
	// them is consistent — "within a tenth of each other" alone is not,
	// three paces a twelfth apart each going round in a circle. Drawn among
	// those the pace orders alone — one running low would otherwise set a
	// band's top and part two alike behind it
	var byPace []int
	for i, s := range seats {
		if seatTier(s) == 0 && !s.Learns {
			byPace = append(byPace, i)
		}
	}
	sort.SliceStable(byPace, func(a, b int) bool { return seats[byPace[a]].Pace > seats[byPace[b]].Pace })
	band, top := make([]int, len(seats)), 0.0
	for k, i := range byPace {
		if p := seats[i].Pace; k == 0 || p < 0.9*top {
			top = p
			if k > 0 {
				band[i] = band[byPace[k-1]] + 1
				continue
			}
		} else {
			band[i] = band[byPace[k-1]]
		}
	}
	left := byLeft(seats, fewerTokens)
	idx := identity(len(seats))
	sort.SliceStable(idx, func(a, b int) bool {
		sa, sb := seats[idx[a]], seats[idx[b]]
		if ta, tb := seatTier(sa), seatTier(sb); ta != tb {
			return ta < tb
		} else if ta == 2 && sa.Credits != sb.Credits {
			return sa.Credits
		} else if ta > 0 {
			if sa.Used != sb.Used {
				return sa.Used < sb.Used
			}
		} else if sa.Learns != sb.Learns {
			return sa.Learns
		} else if band[idx[a]] != band[idx[b]] {
			return band[idx[a]] < band[idx[b]]
		} else if left != nil {
			// a wallet's pace is a whole week's; the one with more left
			// first (#3950)
			if c := left(idx[a], idx[b]); c != 0 {
				return c < 0
			}
		}
		return sa.Tokens < sb.Tokens
	})
	return idx
}}

// Weighted spreads a pool's requests over its seats by each one's weight,
// smoothly (Round): the one the pool's round picks now first, the others
// after it in their order, for failover.
var Weighted = Strategy{Order: func(pool string, seats []Seat) []int {
	keys, weights, rests := make([]string, len(seats)), make([]int, len(seats)), make([]bool, len(seats))
	for i, s := range seats {
		keys[i], weights[i], rests[i] = s.Key, max(s.Weight, 1), s.Resting
	}
	return First(Round(pool, keys, weights, rests), len(seats))
}}

// First is the order of n with best first and the others after it in their
// order; best below 1 leaves it as it is.
func First(best, n int) []int {
	idx := identity(n)
	if best <= 0 {
		return idx
	}
	return append(append([]int{best}, idx[:best]...), idx[best+1:]...)
}

// FreeFirst spends what costs nothing before what is paid for: the seats
// whose model its vendor serves at no cost to the subscription's allowance
// or credits go first, in their listed order, and the rest after them, also
// in their listed order. So a request whose model is free somewhere on the
// pool uses that first, and only when those can't answer does it go to one
// that spends credits or quota — nothing is left unused that cost nothing.
//
// It orders one tier only, unlike Smart: a free seat is not pushed behind a
// paid one for running low, since the free one spends nothing by running
// out of. Among the paid, the listed order stands rather than Smart's
// renewal-weighted one, because what this routing is for is the free tier;
// set a provider's own routing (Smart, in order, weekly pace) if the paid
// seats should be spent smartly among themselves — a provider or group in
// this group keeps its own routing either way.
//
// A seat whose model costs nothing nowhere is not free: it goes with the
// paid ones, so a routing set on a pool without free models behaves as In
// order.
var FreeFirst = Strategy{ByFree: true, Order: func(_ string, seats []Seat) []int {
	free, paid := make([]int, 0, len(seats)), make([]int, 0, len(seats))
	for i, s := range seats {
		if s.Free {
			free = append(free, i)
		} else {
			paid = append(paid, i)
		}
	}
	return append(free, paid...)
}}

// FreeExpiring spends free quota before paid, and of the free, what is
// lost soonest first (wowbbs on Discord: check-in accounts of WorkBuddy,
// MiniMax, Trae and Qoder in one group, each given quota that lapses on
// its own day). A seat is free when its model costs nothing there (Free)
// or its account's plan is a free one (FreePlan). The order is:
//
//   - the free seats with quota left, by when it is lost first, to the
//     hour (Expires: the soonest of its windows' renewals and of its
//     credits' lapse); one that tells neither after those that do, those
//     alike in their listed order;
//   - the paid seats with quota left, as Smart orders them;
//   - the used up, free or paid (a known window all but used up, or
//     resting out of quota or credit), as Smart orders its used up.
//
// So a free account is spent until it is used up or expires, then the
// next free one, and a paid one only once no free one has quota left.
// Rests and failover are as for every routing.
var FreeExpiring = Strategy{ByFree: true, ByPlan: true, Order: func(pool string, seats []Seat) []int {
	var free, paid, spent []int
	for i, s := range seats {
		switch {
		case s.Spent || seatTier(s) == 2:
			spent = append(spent, i)
		case s.Free || s.FreePlan:
			free = append(free, i)
		default:
			paid = append(paid, i)
		}
	}
	sort.SliceStable(free, func(i, j int) bool {
		x, y := Expires(seats[free[i]]).Truncate(time.Hour), Expires(seats[free[j]]).Truncate(time.Hour)
		if x.Equal(y) {
			return false
		}
		if x.IsZero() || y.IsZero() {
			return y.IsZero()
		}
		return x.Before(y)
	})
	if len(paid) > 1 {
		sub := make([]Seat, len(paid))
		for k, i := range paid {
			sub[k] = seats[i]
		}
		by := Smart.Order(pool, sub)
		ordered := make([]int, len(paid))
		for k, i := range by {
			ordered[k] = paid[i]
		}
		paid = ordered
	}
	sort.SliceStable(spent, func(i, j int) bool { return spentBefore(seats[spent[i]], seats[spent[j]]) })
	return append(append(free, paid...), spent...)
}}

// Expires is when the soonest of what a seat has left is lost: the first
// of its windows' renewals (Soon) and its credits' lapse (Lapse), zero
// when it tells neither.
func Expires(s Seat) time.Time {
	var t time.Time
	for _, x := range append(slices.Clip(s.Soon), s.Lapse) {
		if !x.IsZero() && (t.IsZero() || x.Before(t)) {
			t = x
		}
	}
	return t
}

// Cheapest sends a request where its model costs least at the request's
// time: the priced seats by Cost (a million tokens in and a million out),
// those that cost the same by CacheCost, then the seats whose price isn't
// known, each kept in its listed order among those alike. A price that
// changes with the hour (DeepSeek's off-peak half price, a provider's rate
// window) is read at the request's time, so one model on several
// providers goes to the one that bills least now: DeepSeek's own API off
// its peak hours, another provider of the same model in them (liyuanzheng
// and lam_em on Discord). Prices a hair apart (sameCost) count as the
// same, so a rate's rounding doesn't reorder providers billing alike.
//
// Like FreeFirst it orders one tier only: a member's allowance, rests and
// failover are as for every routing, and a provider or group in the group
// routed its own way keeps it.
var Cheapest = Strategy{ByPrice: true, Order: func(_ string, seats []Seat) []int {
	idx := identity(len(seats))
	sort.SliceStable(idx, func(a, b int) bool {
		sa, sb := seats[idx[a]], seats[idx[b]]
		if sa.Priced != sb.Priced {
			return sa.Priced
		}
		if !sa.Priced {
			return false
		}
		if !sameCost(sa.Cost, sb.Cost) {
			return sa.Cost < sb.Cost
		}
		return !sameCost(sa.CacheCost, sb.CacheCost) && sa.CacheCost < sb.CacheCost
	})
	return idx
}}

// sameCost: two prices within a millionth of each other, or of a cent a
// million tokens, are the same price.
func sameCost(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= 1e-6 || d <= 1e-6*max(a, b)
}

// Builtin are the routings besides Smart and their strategies, as
// plugins/rotation registers them.
var Builtin = map[string]Strategy{
	provider.Ordered:      Ordered,
	provider.Rotate:       Rotate,
	provider.LeastUsed:    LeastUsed,
	provider.Pace:         Pace,
	provider.Weighted:     Weighted,
	provider.FreeFirst:    FreeFirst,
	provider.Fastest:      Fastest,
	provider.Cheapest:     Cheapest,
	provider.Drain:        Drain,
	provider.FreeExpiring: FreeExpiring,
}
