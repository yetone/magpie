package gateway

// Fallback: a provider can name models to use when it can't take a request
// — a coding plan out of quota, a rate limit, an overloaded or failing
// vendor. The request goes to the next one only while none of the reply has
// been sent, so an agent sees one clean answer from whoever gave it, never a
// half from each. A provider that just failed that way waits at the back of
// the line for a minute, rather than costing every request a doomed try.
//
// A provider with several keys on is several candidates, one per key, in
// order, and so is a subscription with several accounts on: when one
// account runs out, the next account of the same provider takes the
// request before any fallback model does. A key can be made for one
// protocol only, as some relays hand them out; see perKey.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

const fallbackCooldown = time.Minute

// groupBudget is how long a group's serial walk may wait on members that
// are slow to start, in all: below the 600s a client gives up at, so the
// error comes from magpie and names the budget, and no member is blamed
// for the client hanging up first (#5113). A var for tests.
var groupBudget = 540 * time.Second

type candidate struct {
	p     provider.Provider
	model string
	rest  string // what rests after a failure: the provider, or one of its keys
	// effort is the reasoning the group's member it is of is fixed at
	// ("provider/model:low"); "" for one that follows the agent or the group
	effort string
	// levels are the reasoning levels the member follows the agent among
	// (Member.Levels, "provider/model:low+high"): what the agent asks is
	// sent at the nearest of them. nil for one fixed or following the group.
	levels []string
	// effortMap is, for a member following the group, the level it sends
	// in place of the one asked for (Member.EffortMap); nil sends it as
	// asked
	effortMap map[string]string
	// fast is set on a member the group sends in its vendor's fast mode
	// (Group.Fast); ultra on an agent's fast pick sent Ultrafast (#1954)
	fast, ultra bool
	// rank is its place in its provider's own list of accounts or keys,
	// the order the provider's page shows and a drag sets (#217)
	rank int
	// capped is set on an account left out as held at its usage cap
	capped *capHold
	// noURL is the protocol its provider's keys are all set to that the
	// provider has no URL for (#1702): the provider is tried whole, on the
	// URLs it has, and the routing trace says why
	noURL provider.Protocol
	// seats are its places in the rounds of the groups routed by weight
	// it is in (weightedUnits), for affinity to charge the member that
	// keeps a conversation (weightedKept)
	seats []wrrSeat
	// firstToken is the FirstToken of the innermost group of those the
	// member is in that sets one (#2246): a group in a group times its own
	// members' first tokens, the group asked for only those that set none
	firstToken int
	// patience is the Patience of the innermost group of those the member
	// is in that sets one (#2410): a group in a group waits out its own
	// members' busy failures, the group asked for only those that set none;
	// 0 falls back to the asked group's Waits, PatienceOff skips replan
	patience int
	// member is the member of the group asked for it is of, as the group
	// spells it (Member.Path[0]): a group in the group is one member.
	// place is that member's place in the group's list, 1 for its first,
	// 0 for one not of a group (planGroup): what the user put first is
	// what a member listed after it stands in for (standsInFor)
	member string
	place  int
	// reserve is set, to the provider's AtOnce, on an account or key held
	// back past the first that many (keepAtOnce): tried after them, never
	// given a request for want of a slot (laneMate) nor kept on by a
	// conversation (affine)
	reserve int
}

// capHold is how an account is held at its usage cap: the cap of the
// fullest window at or past its own cap, that window's share used, and when the last such window
// renews (zero when one doesn't say). noCredits: it is a Codex account
// held at 100% as it is set not to spend its credits (cap is 100 then).
type capHold struct {
	cap       int
	used      float64
	back      time.Time
	noCredits bool
	// unread: held by a careful provider as its usage can't be told, why
	// (carefulDoubt), cap then the account's own
	unread string
	// spend: a key held at the spend cap set on it (provider.SpendCap),
	// cap then 100 and used the share of it spent
	spend *spendHold
}

// spendHold is how a key is held at its spend cap: the cap, what magpie
// counted spent in its period, and the period (its start for one in all).
type spendHold struct {
	usd, spent float64
	period     string
	since      time.Time
}

// spendHeld is how the key k of p is held at the spend cap set on it at
// now, nil when it has none or is below it.
func spendHeld(p provider.Provider, k provider.KeyAccount, now time.Time) *capHold {
	sc, ok := p.KeySpendCap(provider.KeyID(k.Key))
	if !ok {
		return nil
	}
	start, reset := sc.Window(now)
	spent := usage.KeySpent(provider.KeyID(k.Key), start)
	if spent < sc.USD {
		return nil
	}
	return &capHold{cap: 100, used: 100 * spent / sc.USD, back: reset, spend: &spendHold{usd: sc.USD, spent: spent, period: sc.Period, since: start}}
}

// label names a candidate in a call's record: the provider, and the key
// when it has several on.
func (c candidate) label() string {
	if c.rest == c.p.ID {
		return c.p.ID
	}
	if c.p.Account != nil {
		return c.p.ID + " (" + c.p.Account.User + ")"
	}
	if c.p.KeyName != "" {
		return c.p.ID + " (" + c.p.KeyName + ")"
	}
	return c.p.ID + " (" + provider.Mask(c.p.Key) + ")"
}

// restKey is what a candidate rests by: an account by its user, whether
// or not the agent is signed in to it — magpie can sign Codex in to the
// next account when the one it is on is out, and the one out stays out.
func (c candidate) restKey() string {
	if a := c.p.Account; a != nil && a.User != "" {
		return c.p.ID + "@" + strings.ToLower(a.User)
	}
	return c.rest
}

// seat is the candidate as one of a group's members has it: its key or
// account, the model, and the effort the member is fixed at.
func (c candidate) seat() string {
	return provider.WithMemberEffort(c.rest+"/"+c.model, cmp.Or(c.effort, strings.Join(c.levels, "+")))
}

func (c candidate) isOpenRouterFree() bool {
	return c.p.Preset == "openrouter" && strings.HasSuffix(c.model, ":free")
}

// restID is the candidate's model's own rest key: a free OpenRouter
// model's, whose rate limit is its free-tier limit, a subscription's, out
// of a pool of its allowance that counts some models only (pooled), and
// any key's or account's model its vendor said it doesn't serve it
// (modelRefused). An account-level rest stays on restKey.
func (c candidate) restID() string {
	return c.restKey() + "/" + c.model
}

// pooled says whether the candidate's subscription, refused for its
// allowance, is out for its model alone (provider.Allowance.Pooled).
func (c candidate) pooled(now time.Time) bool {
	if c.p.Account == nil {
		return false
	}
	return allowances(c.p.Account.UsageAgent())[c.p.Account.User].Pooled(c.model, provider.SpentShareOf(c.p.Routing), now)
}

// who is the key or account itself, however many the provider has on: a
// provider's one key rests as the provider, and as itself once another
// is added, and a conversation it answered stays with it all the same.
// An account is its user, signed in to or not: rest names the one the
// agent is on by the provider's id alone, which a switch of the agent's
// account hands to another (#209).
func (c candidate) who() string {
	if c.p.Account == nil && c.p.Key != "" {
		return c.p.ID + "#" + provider.KeyID(c.p.Key)
	}
	return c.restKey()
}

// remoteUpstream is, for a remote magpie's model, the provider there that
// serves it, its id's first part (anthropic of anthropic/claude-…), or a
// group of the remote's, its whole id; "" for any other provider's model,
// and for a remote's model named without one. The remote magpie is one
// key here, but each of its providers is a vendor of its own, with its
// own cache and its own seals (fremontdango on Discord: a group of one
// model from two of the remote's providers kept a conversation on the
// second when the first was resting, as one account, where the same
// group on the remote moved it as gone).
func remoteUpstream(p provider.Provider, model string) string {
	if !p.IsRemoteMagpie() {
		return ""
	}
	if strings.HasPrefix(model, provider.GroupPrefix) {
		return model
	}
	up, _, _ := strings.Cut(model, "/")
	if up == model {
		return ""
	}
	return up
}

// vendor is who as the vendor a conversation is with knows it: the key or
// account, and behind a remote magpie the provider there (remoteUpstream).
// What a vendor keeps of a conversation — its cache, the reasoning and
// compactions it sealed — goes by it; what the user limits on the key
// here — its lanes, requests a minute — still goes by who.
func (c candidate) vendor() string {
	if up := remoteUpstream(c.p, c.model); up != "" {
		return c.who() + ">" + up
	}
	return c.who()
}

// sameVendor says whether c is with the vendor that who, asked for model,
// was: the same key or account, and behind a remote magpie the same
// provider there. With no model known (an answer the usage log has) it
// is the key or account alone.
func (c candidate) sameVendor(who, model string) bool {
	return c.who() == who && (model == "" || remoteUpstream(c.p, c.model) == remoteUpstream(c.p, model))
}

// perKey is a provider once per key it has on, in order — or, for a
// signed-in agent, once per account it has on, its own first. A key made
// for one protocol only serves on that one's endpoint, and the keys that
// suit the request go first: the Anthropic key for a Claude model, the
// OpenAI one for a GPT model, else the key that speaks what the agent
// spoke, so nothing is translated that needn't be.
func perKey(p provider.Provider, model string, from provider.Protocol) []candidate {
	out, aside, _ := perKeyOf(p, model, from)
	return append(out, aside...)
}

// perKeyOf is perKey, split: the keys routing goes over, those made for
// another protocol, and the accounts or keys it left out as not listing
// the model. Keys made for different protocols are not one pool: routing
// weighs, rotates and keeps conversations over those made for the
// protocol that suits the request best, and the others are tried only
// after them, in the order they suit it.
func perKeyOf(p provider.Provider, model string, from provider.Protocol) (out, aside, left []candidate) {
	out, aside, left, _ = perKeyBarred(p, model, from)
	return out, aside, left
}

// perKeyBarred is perKeyOf, and the accounts or keys the user set not to
// serve the model (provider.AccountModels, #474): never tried, whatever
// else there is — none, when every one of them is.
func perKeyBarred(p provider.Provider, model string, from provider.Protocol) (out, aside, left, barred []candidate) {
	out, aside, left, barred = perKeyEvery(p, model, from)
	out, aside = keepAtOnce(p.AtOnce(), out, aside)
	return out, aside, left, barred
}

// keepAtOnce keeps, of a provider's accounts or keys routed over, the
// first n in the user's own order (rank, the order the provider's page
// shows and a drag sets), and holds the rest back as reserves: tried after
// them, the way a key for another protocol is (aside), and marked so
// (candidate.reserve). 01huadalang on Discord: ZCode bans the address
// that three of its accounts are used from at once, while two are let
// be, and In order alone still spread over all three under load (a full
// lane gave way to the next account, laneMate, and every busy refusal
// rested one). The ones kept are those not resting first, so an account
// at the end of its quota hands its seat to the next in the list, and
// gets it back once it is back; those resting fill seats only when fewer
// than n are not. The user's order, not routing's, says who is first, so
// the set stays the same request to request under In turn or By usage.
// n of 0 keeps them all.
func keepAtOnce(n int, out, aside []candidate) ([]candidate, []candidate) {
	if n <= 0 || len(out)+len(aside) <= n {
		return out, aside
	}
	all := append(slices.Clone(out), aside...)
	byRank := make([]int, len(all))
	for i := range byRank {
		byRank[i] = i
	}
	slices.SortStableFunc(byRank, func(a, b int) int { return all[a].rank - all[b].rank })
	resting := make([]bool, len(all))
	for i, c := range all {
		_, resting[i] = restingOf(c)
	}
	keep := make([]bool, len(all))
	kept := 0
	for _, rested := range []bool{false, true} {
		for _, i := range byRank {
			if kept < n && resting[i] == rested && !keep[i] {
				keep[i], kept = true, kept+1
			}
		}
	}
	var reserves []candidate
	for _, i := range byRank {
		if !keep[i] {
			c := all[i]
			c.reserve = n
			reserves = append(reserves, c)
		}
	}
	keptOf := func(cs []candidate, from int) []candidate {
		var o []candidate
		for i, c := range cs {
			if keep[from+i] {
				o = append(o, c)
			}
		}
		return o
	}
	return keptOf(out, 0), append(keptOf(aside, len(out)), reserves...)
}

// accountsOn are the accounts of p (signed in: p.Account) a request may
// take, in the user's order (provider.AccountsOn). A search API that
// signs in as a plugin's provider takes its accounts so too
// (search_signin.go).
func accountsOn(p provider.Provider, model string) []candidate {
	var all []candidate
	for _, a := range p.AccountsOn() {
		all = append(all, accountCandidate(p, a, model, nil))
	}
	return all
}

// accountCandidate is a of p's accounts as a candidate for model.
func accountCandidate(p provider.Provider, a provider.AccountOn, model string, capped *capHold) candidate {
	rest := p.ID
	if !a.Own {
		rest += "@" + a.Account.User
	}
	return candidate{p: a.Provider, model: model, rest: rest, rank: a.Rank, capped: capped}
}

// keyCandidate is k of p's keys as a candidate for model.
func keyCandidate(p provider.Provider, k provider.KeyOn, model string, capped *capHold) candidate {
	rest := p.ID
	if k.Of > 1 {
		rest += "#" + provider.KeyID(k.Key)
	}
	return candidate{p: p.WithKey(k.KeyAccount), model: model, rest: rest, rank: k.Rank, capped: capped}
}

// perKeyEvery is perKeyBarred with every account or key routed over, the
// provider's AtOnce aside. Which accounts or keys take the model, and in
// what order, is provider.AccountsFor and provider.KeysFor, which a
// model's test takes them from too (#4992); the usage and spend caps are
// the gateway's own.
func perKeyEvery(p provider.Provider, model string, from provider.Protocol) (out, aside, left, barred []candidate) {
	if _, off := provider.ModelDisabled(p.ID, model); off || provider.DisabledUnknown() {
		return nil, nil, nil, nil // the user disabled it (#1867): never planned, a fallback no more than the model asked
	}
	if p.Account != nil {
		// an account at its usage cap is used up for routing until the
		// window it filled renews: never tried, so groups, fallbacks and
		// the other accounts take the request (provider/account_caps.go);
		// so is a Codex account at 100% set not to spend its credits
		// (provider/codex_credits.go)
		now := time.Now()
		caps := map[int]*capHold{}
		on, unlisted, off := p.AccountsFor(model, func(a provider.AccountOn) bool {
			h := capHeld(p, a.Provider, model, now)
			if h != nil {
				caps[a.Rank] = h
			}
			return h != nil
		})
		for _, a := range on {
			out = append(out, accountCandidate(p, a, model, nil))
		}
		for _, a := range unlisted {
			left = append(left, accountCandidate(p, a, model, nil))
		}
		for _, a := range off {
			barred = append(barred, accountCandidate(p, a, model, caps[a.Rank]))
		}
		return out, nil, left, barred
	}
	// a key at the spend cap set on it is used up for routing until the
	// period renews: never tried, so the other keys and the fallbacks
	// take the request (provider/key_spend.go)
	now := time.Now()
	spends := map[int]*capHold{}
	on, unlisted, off, noURL := p.KeysFor(model, func(k provider.KeyOn) bool {
		h := spendHeld(p, k.KeyAccount, now)
		if h != nil {
			spends[k.Rank] = h
		}
		return h != nil
	})
	for _, k := range off {
		barred = append(barred, keyCandidate(p, k, model, spends[k.Rank]))
	}
	if len(on) == 0 && len(barred) > 0 {
		return nil, nil, nil, barred
	}
	if len(on) == 0 {
		// every key is set to a protocol the provider has no URL for (or
		// it has none on): the provider as it is, on the URLs it has,
		// marked so Routing says so rather than sending it there quietly
		return []candidate{{p: p, model: model, rest: p.ID, noURL: noURL}}, nil, nil, nil
	}
	for _, k := range on {
		out = append(out, keyCandidate(p, k, model, nil))
	}
	var rest []candidate
	for _, k := range unlisted {
		rest = append(rest, keyCandidate(p, k, model, nil))
	}
	sort.SliceStable(out, func(i, j int) bool { return keyFit(out[i].p, model, from) < keyFit(out[j].p, model, from) })
	pool := out[:0:0]
	for _, c := range out {
		if c.p.KeyProtocol == out[0].p.KeyProtocol {
			pool = append(pool, c)
		} else {
			aside = append(aside, c)
		}
	}
	return pool, aside, rest, barred
}

// keyFit ranks how well a key suits a request, best first: 0 fits, 1 needs
// the request translated, 2 is made for another vendor's models.
func keyFit(q provider.Provider, model string, from provider.Protocol) int {
	if q.KeyProtocol == "" {
		return 0
	}
	switch modelFamily(model) {
	case provider.Anthropic:
		if q.KeyProtocol == provider.Anthropic {
			return 0
		}
		return 2
	case provider.Chat:
		if q.KeyProtocol != provider.Anthropic && q.KeyProtocol != provider.Gemini {
			return 0
		}
		return 2
	case provider.Gemini:
		// a key made for the Gemini API before any other (corddis_114
		// on Discord); the others keep their order behind it
		if q.KeyProtocol == provider.Gemini {
			return 0
		}
		if q.Base(from) != "" {
			return 1
		}
		return 2
	}
	if q.Base(from) != "" {
		return 0
	}
	return 1
}

// modelFamily is the protocol a model is at home in, when its name says:
// Anthropic for Claude, Chat (standing for OpenAI's) for GPT and the o-series,
// Gemini for Gemini.
func modelFamily(model string) provider.Protocol {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.HasPrefix(m, "claude"):
		return provider.Anthropic
	case strings.HasPrefix(m, "gpt-"), strings.Contains(m, "codex"), len(m) > 1 && m[0] == 'o' && m[1] >= '1' && m[1] <= '9':
		return provider.Chat
	case strings.HasPrefix(m, "gemini"):
		return provider.Gemini
	}
	return ""
}

// candidates is the primary and then its fallbacks, each provider's keys
// or accounts as its routing orders them, those resting after a recent
// failure moved behind the rest.
func (s *Server) candidates(p provider.Provider, model string, from provider.Protocol) []candidate {
	out, _ := s.plan(p, model, from)
	return out
}

// plan is candidates, and for the routing trace what each stood where it
// did by, and those left out.
func (s *Server) plan(p provider.Provider, model string, from provider.Protocol) ([]candidate, planned) {
	return s.planPaused(p, model, from, nil)
}

// planPaused is plan, a routing group among the fallbacks (01huadalang on
// Discord) less the models paused leaves out of it: its pause rules, as
// a request to the group would have them; nil pauses none.
func (s *Server) planPaused(p provider.Provider, model string, from provider.Protocol, paused func(provider.Group, []provider.Member) []provider.Member) ([]candidate, planned) {
	var pl planned
	add := func(q provider.Provider, m string, fallback bool) []candidate {
		cs, aside, left, barred := perKeyBarred(q, m, from)
		pl.left = append(pl.left, barredOf(barred, q, fallback, from, nil)...)
		pl.held = append(pl.held, creditsHeld(barred)...)
		pl.capped = append(pl.capped, capHeldOnly(barred)...)
		cs, wg := weigh(q, cs, m, from)
		for i, c := range cs {
			w := weighed(c, q, wg, fallback, from)
			w.Turn = i == 0 && goesRound(q.Routing) && len(cs) > 1
			pl.order = append(pl.order, w)
		}
		pl.order = append(pl.order, asideOf(aside, q, fallback, from)...)
		cs = append(cs, aside...)
		for _, c := range left {
			w := weighed(c, q, weighing{}, fallback, from)
			w.Unlisted = true
			pl.left = append(pl.left, w)
			pl.unlisted = append(pl.unlisted, c)
			pl.unlistedW = append(pl.unlistedW, w)
		}
		return cs
	}
	out := add(p, model, false)
	if p.CapStop && len(out) == 0 && len(pl.capped)+len(pl.held) > 0 {
		// every account held at its cap: the user set the provider to end
		// there (plugins#349), answered 429 rather than sent on
		return restLast(out, pl)
	}
	seen := map[string]bool{p.ID + "/" + model: true}
	for _, id := range p.Fallback {
		if g, ms, ok := provider.FallbackGroup(id); ok {
			// a routing group: its members as a request to it has them,
			// in its order, routing and sink, less any model tried
			// already (this provider's own, another fallback's); a
			// member's fallbacks are not followed, so nothing leads back
			if paused != nil {
				ms = paused(g, ms)
			}
			ms = slices.DeleteFunc(ms, func(m provider.Member) bool {
				k := m.Provider.ID + "/" + m.Model
				if seen[k] {
					return true
				}
				seen[k] = true
				return false
			})
			out = append(out, planFallbackGroup(g, ms, from, &pl)...)
			continue
		}
		fp, fm, ok := provider.ResolveRoute(id)
		if !ok || seen[fp.ID+"/"+fm] {
			continue
		}
		seen[fp.ID+"/"+fm] = true
		out = append(out, add(fp, fm, true)...)
	}
	return restLast(out, pl)
}

// planFallbackGroup is planGroup for a routing group a provider falls back
// to, its rows added to pl as fallbacks reached through g: the group's
// keys and accounts in its own order, sunk by its own Sink, those set
// aside after them.
func planFallbackGroup(g provider.Group, ms []provider.Member, from provider.Protocol, pl *planned) []candidate {
	if len(ms) == 0 {
		return nil
	}
	at, leftAt, unlistedAt := len(pl.order), len(pl.left), len(pl.unlistedW)
	var asides []candidate
	var wAsides []Weighed
	out := planLevel(g, ms, 0, from, pl, &asides, &wAsides, []provider.Group{g})
	if sinks(g.Sinks(), g.Routing) {
		var order []Weighed
		out, order = sinkPlanned(out, pl.order[at:])
		pl.order = append(pl.order[:at], order...)
	}
	out, pl.order = append(out, asides...), append(pl.order, wAsides...)
	via := func(ws []Weighed) {
		for i := range ws {
			ws[i].Fallback = true
			ws[i].Via = append([]string{g.ID}, ws[i].Via...)
		}
	}
	via(pl.order[at:])
	via(pl.left[leftAt:])
	via(pl.unlistedW[unlistedAt:])
	return out
}

// planGroup is plan for a routing group: every member's keys or accounts
// weighed together as the group's routing says — in order, member by
// member, each as its own provider's routing orders it; else all as one,
// so a subscription whose allowance renews soonest goes first whichever
// provider it is of, but for a provider set to route another way, which
// is planned as a group in the group is (ownRouting). A group in the group is planned by its own routing
// alone, whatever the group's (#576): in order, in its place; else as
// one, weighed with the rest by the one it would try first, and tried
// whole where that one goes — the group's routing picks between its
// groups, never within them. A group in the group sinks by its own Sink
// too, within its own order (#2255), before g's Sink orders the whole.
// A member's fallbacks are not the group's.
func (s *Server) planGroup(g provider.Group, ms []provider.Member, from provider.Protocol) ([]candidate, planned) {
	var pl planned
	var asides []candidate
	var wAsides []Weighed
	out := planLevel(g, ms, 0, from, &pl, &asides, &wAsides, nil)
	if sinks(g.Sinks(), g.Routing) {
		out, pl.order = sinkPlanned(out, pl.order)
	}
	out, pl.order = append(out, asides...), append(pl.order, wAsides...)
	if len(out) == 0 {
		return nil, pl
	}
	placed(ms, out)
	placed(ms, pl.unlisted)
	return restLast(out, pl)
}

// placed sets each of cs's place: where its member is in ms, the group's
// models in the order its list has them, a group in the group by its first.
func placed(ms []provider.Member, cs []candidate) {
	at := map[string]int{}
	for _, m := range ms {
		if len(m.Path) > 0 && at[m.Path[0]] == 0 {
			at[m.Path[0]] = len(at) + 1
		}
	}
	for i := range cs {
		cs[i].place = at[cs[i].member]
	}
}

// innerFirstToken is the FirstToken of the innermost group m is in
// (m.Via, the outermost first) that sets one, 0 for none:
// provider.FirstTokenWait counts as set, so an inner Wait waits.
func innerFirstToken(m provider.Member) int {
	for i := len(m.Via) - 1; i >= 0; i-- {
		if m.Via[i].FirstToken != 0 {
			return m.Via[i].FirstToken
		}
	}
	return 0
}

// innerPatience is the Patience of the innermost group m is in (m.Via,
// the outermost first) that sets one (#2410), 0 for none: PatienceOff
// (-1) counts as set, so an inner Off stops replan for that member.
func innerPatience(m provider.Member) int {
	for i := len(m.Via) - 1; i >= 0; i-- {
		if m.Via[i].Patience != 0 {
			return m.Via[i].Patience
		}
	}
	return 0
}

// planLevel orders the models ms of g, a group depth groups down from the
// one asked for, adding to pl's order as it goes: those set aside and
// those unlisted it gathers for planGroup to put last.
func planLevel(g provider.Group, ms []provider.Member, depth int, from provider.Protocol, pl *planned, asides *[]candidate, wAsides *[]Weighed, hold []provider.Group) []candidate {
	keys := func(m provider.Member) []candidate {
		cs, aside, left, barred := perKeyBarred(m.Provider, m.Model, from)
		// a group the member is reached through that is held to some
		// accounts (Group.Accounts) holds it to them: one it leaves out
		// is said on the plan's left, held, as a gateway key's are
		if by := append(slices.Clip(hold), m.Via...); slices.ContainsFunc(by, func(h provider.Group) bool { return len(h.Accounts) > 0 }) {
			out := func(l []candidate) []candidate {
				return slices.DeleteFunc(l, func(c candidate) bool {
					name := groupHeld(c, by)
					if name == "" {
						return false
					}
					w := weighed(c, m.Provider, weighing{}, false, from)
					w.Held, w.HeldBy, w.Via = true, name, m.Groups()
					pl.left = append(pl.left, w)
					return true
				})
			}
			cs, aside, left, barred = out(cs), out(aside), out(left), out(barred)
		}
		// the effort the member is fixed at goes with each of its keys:
		// the same model at another effort is another member's
		for _, l := range [][]candidate{cs, aside, left, barred} {
			for i := range l {
				l[i].effort, l[i].levels, l[i].effortMap, l[i].fast = m.Effort, m.Levels, m.EffortMap, m.Fast
				l[i].firstToken = innerFirstToken(m)
				l[i].patience = innerPatience(m)
				if len(m.Path) > 0 {
					l[i].member = m.Path[0]
				}
			}
		}
		pl.left = append(pl.left, barredOf(barred, m.Provider, false, from, m.Groups())...)
		pl.held = append(pl.held, creditsHeld(barred)...)
		pl.capped = append(pl.capped, capHeldOnly(barred)...)
		*asides = append(*asides, aside...)
		for _, w := range asideOf(aside, m.Provider, false, from) {
			w.Via = m.Groups()
			*wAsides = append(*wAsides, w)
		}
		for _, c := range left {
			w := weighed(c, m.Provider, weighing{}, false, from)
			w.Unlisted, w.Via = true, m.Groups()
			pl.left = append(pl.left, w)
			pl.unlisted = append(pl.unlisted, c)
			pl.unlistedW = append(pl.unlistedW, w)
		}
		return cs
	}
	if routingOf(g.Routing) != provider.Ordered {
		// what the group weighs: each of its models' keys or accounts,
		// and each group in it as one — planned by its own routing, put
		// where the one it would try first is, the rest of it after
		type unit struct {
			m      provider.Member // the model, for one of its keys
			cs     []candidate     // a group in g, or a provider routed its own way: its own order
			order  []Weighed
			member string // as g spells it, for a unit of a group routed by weight
		}
		// a group routed by weight weighs its members, each whole: a
		// model's keys in its provider's order, a group in it in its own
		byWeight := routingOf(g.Routing) == provider.Weighted
		var units []unit
		var heads []candidate
		at := map[string][]int{} // a head's seat → its units, in turn
		add := func(u unit, head candidate) {
			at[head.seat()] = append(at[head.seat()], len(units))
			units, heads = append(units, u), append(heads, head)
		}
		for i := 0; i < len(ms); {
			m := ms[i]
			if len(m.Path) > depth+1 {
				j := i + 1
				for j < len(ms) && len(ms[j].Path) > depth+1 && ms[j].Path[depth] == m.Path[depth] {
					j++
				}
				var sub planned
				cs := planLevel(m.Via[depth], ms[i:j], depth+1, from, &sub, asides, wAsides, hold)
				cs, sub.order = sinkOwn(m.Via[depth], cs, sub.order)
				pl.left = append(pl.left, sub.left...)
				pl.capped = append(pl.capped, sub.capped...)
				pl.unlisted = append(pl.unlisted, sub.unlisted...)
				pl.unlistedW = append(pl.unlistedW, sub.unlistedW...)
				if len(cs) > 0 {
					add(unit{cs: cs, order: sub.order, member: m.Path[depth]}, firstAwake(cs))
				}
				i = j
				continue
			}
			if ownRouting(m.Provider, g) || byWeight {
				// a provider routed its own way keeps it, as a group
				// in the group does: weighed as one by the first it
				// would try, and tried whole in its own order (kchen0x
				// on X: Antigravity in order inside a Smart group)
				cs, wg := weigh(m.Provider, keys(m), m.Model, from)
				var order []Weighed
				for k, c := range cs {
					w := weighed(c, m.Provider, wg, false, from)
					w.Turn = k == 0 && goesRound(m.Provider.Routing) && len(cs) > 1
					w.Via = m.Groups()
					order = append(order, w)
				}
				if len(cs) > 0 {
					add(unit{cs: cs, order: order, member: m.Path[depth]}, firstAwake(cs))
				}
				i++
				continue
			}
			for _, c := range keys(m) {
				add(unit{m: m}, c)
			}
			i++
		}
		if byWeight {
			members := make([]string, len(units))
			for i, u := range units {
				members[i] = u.member
			}
			var out []candidate
			for i, k := range weightedUnits(g, members, heads) {
				u := units[k]
				seat := wrrSeat{round: provider.GroupPrefix + g.ID, member: u.member}
				for _, c := range u.cs {
					c.seats = append(slices.Clip(c.seats), seat)
					out = append(out, c)
				}
				order := slices.Clone(u.order)
				if i == 0 && len(units) > 1 && len(order) > 0 {
					order[0].Turn = true
				}
				pl.order = append(pl.order, order...)
			}
			return out
		}
		routing := g.Routing
		if routing == provider.Manual {
			routing = "" // the member picked, its keys or accounts weighed smartly
		}
		weighedHeads, wg := weigh(provider.Provider{ID: provider.GroupPrefix + g.ID, Routing: routing}, heads, "", from)
		var out []candidate
		for i, c := range weighedHeads {
			k := at[c.seat()][0]
			at[c.seat()] = at[c.seat()][1:]
			u := units[k]
			if u.cs != nil {
				out = append(out, u.cs...)
				pl.order = append(pl.order, u.order...)
				continue
			}
			w := weighed(c, u.m.Provider, wg, false, from)
			w.Routing, w.Via = g.Routing, u.m.Groups()
			w.Sunk = nil // weighed as the group's, whose own Sink tells
			w.Turn = i == 0 && routingOf(g.Routing) == provider.Rotate && len(weighedHeads) > 1
			pl.order = append(pl.order, w)
			out = append(out, c)
		}
		return out
	}
	var out []candidate
	for i := 0; i < len(ms); {
		m := ms[i]
		if len(m.Path) > depth+1 {
			// a group in g: its models, planned by its routing and
			// sunk by its Sink
			j := i + 1
			for j < len(ms) && len(ms[j].Path) > depth+1 && ms[j].Path[depth] == m.Path[depth] {
				j++
			}
			at := len(pl.order)
			cs := planLevel(m.Via[depth], ms[i:j], depth+1, from, pl, asides, wAsides, hold)
			var order []Weighed
			cs, order = sinkOwn(m.Via[depth], cs, pl.order[at:])
			pl.order = append(pl.order[:at], order...)
			out = append(out, cs...)
			i = j
			continue
		}
		cs, wg := weigh(m.Provider, keys(m), m.Model, from)
		for k, c := range cs {
			w := weighed(c, m.Provider, wg, false, from)
			w.Turn = k == 0 && goesRound(m.Provider.Routing) && len(cs) > 1
			w.Via = m.Groups()
			pl.order = append(pl.order, w)
		}
		out = append(out, cs...)
		i++
	}
	return out
}

// sinkOwn is cs, the keys and accounts of sub, a group in a group, as
// planLevel planned them, with those that sank behind the others when
// sub's own Sink is on, and order, what the trace says of each, kept with
// them (#2255, ggbdpq): a nested group is planned by its own routing
// (#576) and rules, and its Sink was left unread, only the group the
// agent asked for sinking.
func sinkOwn(sub provider.Group, cs []candidate, order []Weighed) ([]candidate, []Weighed) {
	if !sinks(sub.Sinks(), sub.Routing) {
		return cs, order
	}
	return sinkPlanned(cs, order)
}

// firstAwake is the first of cs that isn't resting, else the first: what
// a group in a group, or a provider routed its own way, is weighed by.
func firstAwake(cs []candidate) candidate {
	for _, c := range cs {
		if _, resting := restOf(c.restKey()); !resting {
			if _, resting = restOf(c.restID()); !resting {
				return c
			}
		}
	}
	return cs[0]
}

// ownRouting: p, a model's provider in g, is routed by its own routing
// rather than weighed key by key with the rest of g. One left on Smart,
// the default, or routed as g is, is weighed with g's other keys and
// accounts as one pool, so a subscription whose allowance renews soonest
// goes first whichever provider it is of; one set another way keeps it,
// as the user set it, whatever g's routing.
func ownRouting(p provider.Provider, g provider.Group) bool {
	return p.Routing != "" && p.Routing != g.Routing
}

// capHeld is how the account acct of p is held at its usage cap for
// model at now — a window at or past its own cap, else the account's — or
// at 100% as a Codex account set not to spend its credits; nil when
// nothing holds it or every window is below its share. Its windows
// are those last read (allowances), as smart routing weighs them.
func capHeld(p, acct provider.Provider, model string, now time.Time) *capHold {
	if acct.Account == nil {
		return nil
	}
	a := acct.Account
	caps := provider.HoldCaps(p, a.Agent, a.User)
	if a.Agent == "codex" && freeOnChatGPT(model, a.Plan) {
		// a call ChatGPT counts in no window spends nothing a usage cap
		// keeps (#1593); credits set not to be spent still hold it at
		// 100%, as nothing says the backend answers it there
		caps = provider.WindowCaps{Credits: caps.Credits}
	}
	if !caps.Holds() {
		return nil
	}
	al, known := allowances(a.UsageAgent())[a.User]
	h := al.CapHeld(model, caps, now)
	if h == nil {
		if p.CapCareful {
			// what can't be told is not spent (plugins#349)
			if why := carefulDoubt(a, al, known, model, caps, now); why != "" {
				used, _ := al.For(model, now)
				return &capHold{cap: capShare(caps), used: used, unread: why}
			}
		}
		return nil
	}
	return &capHold{cap: h.Cap, used: h.Used, back: h.Back, noCredits: h.Credits}
}

// cappedError says why a request for model went nowhere when every account
// that could take it is held at its usage cap, as the vendor's own refusal
// would when all are used up; the soonest one is back is the wait.
func cappedError(model string, ws []Weighed, now time.Time) (string, time.Time) {
	held, soonest := heldReasons(ws, now)
	if spendOnly(ws) {
		again := "once that period renews"
		if soonest.IsZero() {
			again = "once its cap is raised"
		}
		return fmt.Sprintf("spend cap reached: every key that serves %q has spent the spend cap set on it in magpie — %s. magpie uses it again %s; %s", model, strings.Join(held, "; "), again, spendAdvice), soonest
	}
	capped, noCredits := heldBy(ws)
	if slices.ContainsFunc(ws, func(w Weighed) bool { return w.SpendCap > 0 }) {
		msg, _ := cappedError(model, slices.DeleteFunc(slices.Clone(ws), func(w Weighed) bool { return w.SpendCap > 0 }), now)
		var spends []string
		for _, w := range ws {
			if w.SpendCap > 0 {
				r, _ := heldReasons([]Weighed{w}, now)
				spends = append(spends, r...)
			}
		}
		return msg + "; and " + strings.Join(spends, "; ") + " (to use it, " + spendAdvice + ")", soonest
	}
	if unread(ws) {
		return fmt.Sprintf("usage cap reached: every account that serves %q is held at the usage cap set on it in magpie, or held as its usage can't be told right now — %s. magpie reads it again at the next request and uses it once it is under its cap; %s, or turn off Check usage near the cap (Providers → the provider's accounts, or magpie provider pool <id> --careful off)", model, strings.Join(held, "; "), heldAdvice(capped, noCredits)), soonest
	}
	switch {
	case !noCredits:
		return fmt.Sprintf("usage cap reached: every account that serves %q is held at the usage cap set on it in magpie — %s. magpie uses it again once that window renews; %s", model, strings.Join(held, "; "), heldAdvice(capped, noCredits)), soonest
	case !capped:
		return fmt.Sprintf("usage limit reached: every account that serves %q has used up its allowance and is set in magpie not to spend its credits — %s. magpie uses it again once that window renews; %s", model, strings.Join(held, "; "), heldAdvice(capped, noCredits)), soonest
	}
	return fmt.Sprintf("usage limit reached: every account that serves %q is held by magpie — %s. magpie uses it again once that window renews; %s", model, strings.Join(held, "; "), heldAdvice(capped, noCredits)), soonest
}

// checkpointCappedError is cappedError for a request whose native
// compaction only the account that sealed it can read (#1099), when that
// one is held: the other accounts that serve model weren't tried for it,
// not because they are held too (#2517).
func checkpointCappedError(model string, cp checkpointWriter, ws []Weighed, now time.Time) (string, time.Time) {
	held, soonest := heldReasons(ws, now)
	capped, noCredits := heldBy(ws)
	return fmt.Sprintf("usage limit reached on the one account this conversation can go to — %s. The conversation was compacted natively by %s, and only that key or account can read the compaction it sealed, so magpie doesn't send it to another account that serves %q, which would refuse it; the routing mode and how conversations stay don't change that. magpie uses it again once that window renews; %s, or start a new conversation", strings.Join(held, "; "), cp.Who, model, heldAdvice(capped, noCredits)), soonest
}

// heldReasons is what holds each account of ws held at a share, and the
// soonest one is back.
func heldReasons(ws []Weighed, now time.Time) ([]string, time.Time) {
	var held []string
	var soonest time.Time
	for _, w := range ws {
		if w.Capped == 0 {
			continue
		}
		s := fmt.Sprintf("%s (%s) is at %.0f%% of a usage window, past its %d%% cap", w.Name, w.Who, w.Used, w.Capped)
		if w.SpendCap > 0 {
			s = fmt.Sprintf("%s (%s) has spent %s of the %s spend cap set on it %s, as magpie's usage prices its calls", w.Name, w.Who, usd(w.Spent), usd(w.SpendCap), spendPeriod(w.SpendPeriod, w.SpendSince, now))
		}
		if w.NoCredits {
			s = fmt.Sprintf("%s (%s) has used up a usage window and is set not to spend its credits", w.Name, w.Who)
		}
		if w.Unread != "" {
			s = fmt.Sprintf("%s (%s) is held, as %s and its provider is set to check usage near the cap", w.Name, w.Who, w.Unread)
		}
		if w.CapBack != nil {
			s += ", until " + heldClock(*w.CapBack, now)
			if soonest.IsZero() || w.CapBack.Before(soonest) {
				soonest = *w.CapBack
			}
		}
		held = append(held, s)
	}
	return held, soonest
}

// spendAdvice is how to go on past a key's spend cap.
const spendAdvice = "raise or lift the key's spend cap in magpie (Providers → the key's spend cap, or magpie provider spend-cap), or add a fallback"

// spendOnly says whether every one of ws held is held at a key's spend
// cap.
func spendOnly(ws []Weighed) bool {
	n := 0
	for _, w := range ws {
		if w.Capped == 0 {
			continue
		}
		if w.SpendCap == 0 {
			return false
		}
		n++
	}
	return n > 0
}

// usd is an amount in dollars as a spend cap is told: cents kept.
func usd(v float64) string {
	return "$" + strconv.FormatFloat(math.Floor(v*100+0.5)/100, 'f', 2, 64)
}

// spendPeriod is the period a spend cap counts over, as a reason tells it.
func spendPeriod(period string, since *time.Time, now time.Time) string {
	switch period {
	case provider.SpendMonth:
		return "for this month"
	case provider.SpendTotal:
		if since == nil || since.IsZero() {
			return "in all"
		}
		return "in all since " + since.In(now.Location()).Format("Jan 2 2006")
	}
	return "for today"
}

// heldAdvice is how to go on now past what heldBy says holds.
func heldAdvice(capped, noCredits bool) string {
	switch {
	case !noCredits:
		return "raise or lift the cap in magpie (Providers → the account's cap, or magpie provider account-cap)"
	case !capped:
		return "to go on now, if an account holds credits, let it spend them (Usage → the account's Use credits, or magpie quota credits <account> on)"
	}
	return "to go on now, raise or lift a cap (Providers → the account's cap, or magpie provider account-cap) or, if an account holds credits, let it spend them (Usage → the account's Use credits, or magpie quota credits <account> on)"
}

// heldBy says what holds the accounts of ws held at a share: a usage cap
// set on one, credits it is set not to spend, or both.
func heldBy(ws []Weighed) (capped, noCredits bool) {
	for _, w := range ws {
		if w.Capped > 0 {
			capped, noCredits = capped || !w.NoCredits, noCredits || w.NoCredits
		}
	}
	return capped, noCredits
}

// checkpointCappedRecord is the call's record of checkpointCappedError's
// refusal, told apart as cappedRecord's.
func checkpointCappedRecord(ws []Weighed) string {
	switch capped, noCredits := heldBy(ws); {
	case !noCredits:
		return "native compaction's account at its usage cap"
	case !capped:
		return "native compaction's account held: set not to spend its credits"
	}
	return "native compaction's account held: at its usage cap or set not to spend its credits"
}

// cappedRecord is the call's record of the same refusal as cappedError,
// told apart the same way.
func cappedRecord(ws []Weighed) string {
	if spendOnly(ws) {
		return "every key at its spend cap"
	}
	if unread(ws) {
		return "every account at its usage cap or its usage unread"
	}
	switch capped, noCredits := heldBy(ws); {
	case !noCredits:
		return "every account at its usage cap"
	case !capped:
		return "every account held: set not to spend its credits"
	}
	return "every account held: at its usage cap or set not to spend its credits"
}

// unread says whether one of ws is held as its usage can't be told
// (carefulDoubt).
func unread(ws []Weighed) bool {
	for _, w := range ws {
		if w.Capped > 0 && w.Unread != "" {
			return true
		}
	}
	return false
}

// creditsHeld are those of cs held only as they won't spend their
// credits: out of their allowance at the vendor's own 100%, where one
// that spends its resets by itself spends one (codex_autoreset.go). One
// held at the user's cap never does.
func creditsHeld(cs []candidate) []candidate {
	var out []candidate
	for _, c := range cs {
		if c.capped != nil && c.capped.noCredits {
			out = append(out, c)
		}
	}
	return out
}

// capHeldOnly are those of cs held at the usage cap the user set on them,
// not for credits they won't spend: a conversation kept strictly on one
// goes on there past the cap (provider.AffinityStrict, #1797).
func capHeldOnly(cs []candidate) []candidate {
	var out []candidate
	for _, c := range cs {
		if c.capped != nil && !c.capped.noCredits && c.capped.spend == nil {
			out = append(out, c)
		}
	}
	return out
}

// barredOf is how the trace tells the accounts or keys the user set not
// to serve the model: left out, as those not listing it are.
func barredOf(cs []candidate, q provider.Provider, fallback bool, from provider.Protocol, via []string) []Weighed {
	var out []Weighed
	for _, c := range cs {
		w := weighed(c, q, weighing{}, fallback, from)
		w.Unlisted, w.Via = true, via
		if h := c.capped; h != nil {
			w.Capped, w.Used, w.NoCredits, w.Unread = h.cap, h.used, h.noCredits, h.unread
			if sp := h.spend; sp != nil {
				w.SpendCap, w.Spent, w.SpendPeriod = sp.usd, sp.spent, sp.period
				if sp.period == provider.SpendTotal {
					w.SpendSince = &sp.since
				}
			}
			if !h.back.IsZero() {
				w.CapBack = &h.back
			}
		} else {
			w.Barred = true
		}
		out = append(out, w)
	}
	return out
}

// barredError says why a request for model went nowhere when every account
// or key that could take it was set not to serve it.
func barredError(model string, ws []Weighed) string {
	var names []string
	for _, w := range ws {
		if !slices.Contains(names, w.Name) {
			names = append(names, w.Name)
		}
	}
	return fmt.Sprintf("model %q is set not to be served by any account or key of %s: each one's own list of models leaves it out. Add it to an account's models in magpie (Providers → the account's Models), or pick another model", model, strings.Join(names, ", "))
}

// asideOf is how the trace tells the keys made for another protocol than
// those routed over: after them, in the order they suit the request.
func asideOf(cs []candidate, q provider.Provider, fallback bool, from provider.Protocol) []Weighed {
	var out []Weighed
	for _, c := range cs {
		w := weighed(c, q, weighing{}, fallback, from)
		w.Aside = true
		out = append(out, w)
	}
	return out
}

// restLast moves those resting after a recent failure behind the rest,
// and a Codex account set to spend its credits last that has gone onto
// them (creditsLast) behind both, those in their own order. Ahead of the
// resting, a model its plugin says is busier than the user's
// settings.BusyLoad (busyOf) goes behind the ready ones: it answers, only
// after a wait in its vendor's queue (axelburks on Discord).
func restLast(out []candidate, pl planned) ([]candidate, planned) {
	if len(out) == 1 {
		if r, ok := restingOf(out[0]); ok {
			pl.order[0].Rest = &r // tried all the same: there is no other
		}
		pl.order[0].CreditsLast = out[0].creditsLast(time.Now())
		return out, pl
	}
	now := time.Now()
	over := settings.Shared().BusyLoad
	var ready, busy, resting, last []candidate
	var wReady, wBusy, wResting, wLast []Weighed
	for i, c := range out {
		r, isResting := restingOf(c)
		if isResting {
			pl.order[i].Rest = &r
		}
		load, isBusy := busyOf(c, over, now)
		switch {
		case c.creditsLast(now):
			pl.order[i].CreditsLast = true
			last, wLast = append(last, c), append(wLast, pl.order[i])
		case isResting:
			resting, wResting = append(resting, c), append(wResting, pl.order[i])
		case isBusy:
			pl.order[i].Busy = load
			busy, wBusy = append(busy, c), append(wBusy, pl.order[i])
		default:
			ready, wReady = append(ready, c), append(wReady, pl.order[i])
		}
	}
	pl.order = append(append(append(wReady, wBusy...), wResting...), wLast...)
	return append(append(append(ready, busy...), resting...), last...), pl
}

// busyOf is the load c's plugin last reported for its model, and whether
// that is over the percent over (settings.BusyLoad; 0 is off). A model
// with no load reported, or none within plugin.LoadFresh, isn't busy:
// unknown is never taken for idle nor for busy. The plugin reports it by
// its own id of the model, so a model the user lists by another is looked
// up by that (axelburks on Discord).
func busyOf(c candidate, over int, now time.Time) (float64, bool) {
	if over <= 0 {
		return 0, false
	}
	load, _, ok := plugin.ModelLoad(c.p.PluginProvider(), c.p.OwnModelID(c.model), now)
	return load, ok && load > float64(over)
}

// probeOne keeps, of the candidates resting after a failure, only the one
// back soonest (the first in order on a tie), at its place; the ready ones
// all stay (#1799). Before, every request asked each resting member in
// turn once those ahead of it failed, so a member that was down was asked
// by every request its rest was meant to spare it from, and in an outage
// of the whole group each request went to all of them. Now a request asks
// at most one resting member: the one whose rest is nearest its end,
// whose answer tells whether it is back, as AxonHub's error-aware
// balancing lets a failed channel's penalty decay rather than send it
// every request. A lone candidate is always kept, so a request still gets
// a real answer, and nothing about the request's own content or model
// rests a member (only the model, by restID), so it isn't left out over
// those. The trace keeps every member, the resting ones with their rest.
func probeOne(cs []candidate) []candidate {
	if len(cs) < 2 {
		return cs
	}
	soonest := -1
	var until time.Time
	rests := 0
	rest := make([]bool, len(cs))
	for i, c := range cs {
		r, ok := restOf(c.restKey())
		if !ok && c.restID() != c.restKey() {
			r, ok = restOf(c.restID())
		}
		if !ok {
			continue
		}
		rests, rest[i] = rests+1, true
		if soonest < 0 || r.Until.Before(until) {
			soonest, until = i, r.Until
		}
	}
	if rests < 2 {
		return cs
	}
	out := make([]candidate, 0, len(cs)-rests+1)
	for i, c := range cs {
		if i == soonest || !rest[i] {
			out = append(out, c)
		}
	}
	return out
}

// restingOf is why c rests, by its own key or else its provider's, while
// it does. A key resting out of its own windows has them read again as it
// is planned, behind the request and once a minute at most: an ordered
// group weighs nothing by them, and a reading that finds them full no
// more is what brings it back (renewed).
func restingOf(c candidate) (Rest, bool) {
	r, ok := restOf(c.restKey())
	if !ok && c.restID() != c.restKey() {
		r, ok = restOf(c.restID())
	}
	if ok && r.agent == "" && r.user != "" {
		keyAllowance(c.p)
	}
	return r, ok
}

// quotaHeld is, when every candidate of cs is a key resting out of its
// allowance or its credit — refused with a 429 or a 402 for it, the
// vendor's words kept — the rest of the first, and when the first of them
// is asked again: asked before then, each would be refused the same
// (#1772). A key whose rest magpie guessed the length of, the vendor and
// the user naming no time, is asked again all the same after its first
// refusal, and a minute on after the next (Rest.asksAt): zoecats on Discord, a local aggregator said "quota
// exhausted" for the moment it switched upstreams, and every request,
// the agent's retry first, was answered from its 15-minute rest. A
// subscription account isn't held so: its resets may be spent for it
// (autoReset), and its windows are read again as it is planned. Nor is a
// model of another computer's magpie (Rest.there): that magpie holds its
// own rests, and the user may have changed what it serves since
// (zaidu259434 on X: a group there moved off a spent vendor, and its
// refusal was still given here until the time it named).
func quotaHeld(cs []candidate) (Rest, time.Time, bool) {
	var first Rest
	var back time.Time
	now := time.Now()
	for i, c := range cs {
		r, ok := restingOf(c)
		if !ok || c.p.Account != nil || r.there || r.Why != failQuota && r.Why != failCredit || !quotaStatus(r.Status) || r.said == "" {
			return Rest{}, time.Time{}, false
		}
		at := r.asksAt(now)
		if !now.Before(at) {
			return Rest{}, time.Time{}, false
		}
		if i == 0 {
			first = r
		}
		if back.IsZero() || at.Before(back) {
			back = at
		}
	}
	return first, back, len(cs) > 0
}

// quotaStatus is a status a vendor says a key is out of its allowance with.
func quotaStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusPaymentRequired
}

// soonestBack is when the first of cs is back from its rest, or asked
// again before it is (Rest.asksAt); false when one of them isn't resting.
func soonestBack(cs []candidate) (time.Time, bool) {
	var back time.Time
	now := time.Now()
	for _, c := range cs {
		r, ok := restingOf(c)
		if !ok {
			return time.Time{}, false
		}
		if at := r.asksAt(now); back.IsZero() || at.Before(back) {
			back = at
		}
	}
	return back, !back.IsZero()
}

// spentAfter says whether every candidate in cs rests with its allowance
// run out: none of them is likely to answer.
func spentAfter(cs []candidate) bool {
	for _, c := range cs {
		r, ok := restOf(c.restKey())
		if !ok && c.restID() != c.restKey() {
			r, ok = restOf(c.restID())
		}
		if !ok || r.Why != failQuota && r.Why != failCredit {
			return false
		}
	}
	return len(cs) > 0
}

// spentModel says whether every account or key routing would try for
// p's model is out of its allowance as last known: held at its usage cap,
// resting after the vendor said it is out of quota or credit (the
// account's, or its model's own pool), or with a window that counts the
// model used up. back is when the first of them is, zero when none says.
// A model magpie picks for a call of its own (AutoVision, autoSearcher)
// is one routing would still try: a turn's own model goes to the one
// candidate it has all the same, but the describer or searcher picked in
// its place can be another (#1801). Unknown allowance, or a rest for a
// rate limit or an error, is not spent.
func spentModel(p provider.Provider, model string, from provider.Protocol, now time.Time) (back time.Time, spent bool) {
	out, aside, _, barred := perKeyBarred(p, model, from)
	soonest := func(t time.Time) {
		if !t.IsZero() && (back.IsZero() || t.Before(back)) {
			back = t
		}
	}
	for _, c := range barred {
		if c.capped != nil {
			soonest(c.capped.back)
		}
	}
	for _, c := range append(out, aside...) {
		if r, ok := restingOf(c); ok && (r.Why == failQuota || r.Why == failCredit) {
			soonest(r.Until)
			continue
		}
		if t := c.full(now); !t.IsZero() {
			soonest(t)
			continue
		}
		return time.Time{}, false
	}
	return back, true
}

var restingUntil = struct {
	sync.Mutex
	m    map[string]time.Time
	note map[string]Rest // why each rests
	// lifted is when each rest was lifted before its time — by hand
	// (Unrest), or its windows renewed — by the key it rested by: the
	// kept routes still say it rested, and the Routing page reads this
	// (TraceState.Lifted) to stop showing a rest lifted since it was
	// found, from whichever page lifted it (zoecats on Discord: Try now
	// in the provider's editor left Routing at 429 until a restart)
	lifted map[string]time.Time
}{m: map[string]time.Time{}, note: map[string]Rest{}, lifted: map[string]time.Time{}}

// liftedLocked notes that the rest by key was lifted now, with
// restingUntil held. A lift older than any rest can be is let go.
func liftedLocked(key string, now time.Time) {
	for k, at := range restingUntil.lifted {
		if now.Sub(at) > longestMonthly {
			delete(restingUntil.lifted, k)
		}
	}
	restingUntil.lifted[key] = now
}

// liftedRests is when each rest lifted before its time was lifted, by
// the key it rested by.
func liftedRests() map[string]time.Time {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	if len(restingUntil.lifted) == 0 {
		return nil
	}
	out := make(map[string]time.Time, len(restingUntil.lifted))
	for k, at := range restingUntil.lifted {
		out[k] = at
	}
	return out
}

func (s *Server) resting(id string) bool {
	_, ok := restOf(id)
	return ok
}

// restOf is why a candidate is resting, while it is.
func restOf(id string) (Rest, bool) {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	until := restingUntil.m[id]
	if !time.Now().Before(until) {
		return Rest{}, false
	}
	r := restingUntil.note[id]
	r.Until = until
	return r, true
}

// quotaWords are how vendors say "out of quota" or "slow down" when their
// status code doesn't: some answer 400 or 403 with it.
var quotaWords = regexp.MustCompile(`(?i)quota|insufficient|balance|credit|billing|out of budget|budget (exceeded|exhausted)|exceeded|rate.?limit|usage.?limit|limit.?reached|hit your .*limit|limit.{0,24}resets|too many requests|overloaded|余额|额度|欠费|限流|频率|频繁|速率|套餐|用量|上限`)

// quotaSaid is whether an error's words, with no status to go by, say
// out of quota or slow down: quotaWords, unless they say the conversation
// is too long for the model, whose "context_length_exceeded" or "exceeds
// the maximum" holds quotaWords' "exceeded" (#1944).
func quotaSaid(msg string) bool {
	return quotaWords.MatchString(msg) && !tooLong(http.StatusBadRequest, msg)
}

// unservedWords are how a vendor says the model isn't one it serves this
// key, or this way — words another provider, or key, may not answer with.
// provider.ModelUnserved owns them: a plugin's 401 that says them doesn't
// mark the account's sign-in either.
var unservedWords = provider.ModelUnserved

// modelRefused says the vendor turned the request away over its model
// alone — not one this key's plan, or this key, may use: SenseNova's 403
// "model is not available in the current token plan" for
// deepseek-v4.1-flash, while the same key serves deepseek-v4-flash
// (#1235). The key or account isn't at fault, so only its model rests,
// and its other models are asked as before. A refusal that also says
// quota, credit or a rate limit is about the account, and rests it.
func modelRefused(status int, body []byte) bool {
	if modelRetired(status, body) {
		return true
	}
	switch status {
	case 400, 403, 404, 422:
	default:
		return false
	}
	// the words are matched decoded, too (#1669)
	body = wordsOf(body)
	return unservedWords.Match(body) && !quotaWords.Match(body) && !refusedWords.Match(body) && !zdrRefused(status, body)
}

// codeAssistModelMissing says a Google account's Code Assist — the
// Antigravity or Gemini CLI built-in's — answered 404 NOT_FOUND
// ("Requested entity was not found."). Its endpoint is the same for every
// model, so what isn't there is the model: one fetchAvailableModels lists
// that the account's plan doesn't serve (Packing1 on Discord: Argon on
// Google AI Pro). Only that model rests, on that account; its others are
// asked as before.
func codeAssistModelMissing(p provider.Provider, status int, body []byte) bool {
	if status != http.StatusNotFound || p.Account == nil || p.IsPlugin() {
		return false
	}
	if p.Account.Agent != "antigravity" && p.Account.Agent != "gemini" {
		return false
	}
	return bytes.Contains(body, []byte("NOT_FOUND")) || bytes.Contains(bytes.ToLower(body), []byte("not found"))
}

// retiredWords are how a vendor says the model is retired, though its
// list may still name it: OpenCode Zen's 410 {"type":"error","error":
// {"type":"ModelDeprecated","message":"Model exo-free has been
// deprecated."}} (MOMO on Discord), OpenAI's "The model … has been
// deprecated".
var retiredWords = regexp.MustCompile(`(?i)"ModelDeprecated"|model_deprecated|model.{0,80}(has been|was|is now|is) (deprecated|retired|discontinued|decommissioned|sunset)`)

// modelRetired says the vendor turned the request away because the model
// is retired there: it leaves that provider's place in the lists
// (provider.Retire), only it rests, and another provider of it, or the
// group's next member, is asked. Not when it also says quota or credit.
func modelRetired(status int, body []byte) bool {
	switch status {
	case 400, 404, 410, 422:
	case 401:
		// OpenCode Zen's word for a model it serves no more, said with
		// its sign-in status: the key is good (a bad one is AuthError)
		return zenModelGone.Match(body)
	default:
		return false
	}
	// the safety filter turning the request away (#3274) is about the
	// request, whatever its message says of the model
	if _, refused := policyRefusal(body); refused {
		return false
	}
	// the words are matched decoded, too (#1669)
	body = wordsOf(body)
	return retiredWords.Match(body) && !quotaWords.Match(body)
}

// zenModelGone is OpenCode Zen's 401 for a model it no longer serves,
// as it answered glm-5-free and kimi-k2.5-free on 2026-10-10 once they
// left its free list: {"type":"error","error":{"type":"ModelError",
// "message":"Model glm-5-free is not supported"}}. Read in the vendor's
// body, or in the message magpie put it into ("OpenCode Zen Free: Model
// glm-5-free is not supported"). Not its "is not supported for format
// anthropic", a model served on another API (wrongEndpoint), and not its
// AuthError "Invalid API key." for a key it doesn't take.
var zenModelGone = regexp.MustCompile(`(^|[\s:"])Model [^"\s]+ is not supported("|\s*$)`)

// refusedWords are how a vendor says it won't take requests from this
// client at all — WorkBuddy's "Illegal API invocation from an unapproved
// channel" to a chat opening with another agent's own system prompt — a
// refusal of the provider, not of the request, another member may serve.
var refusedWords = regexp.MustCompile(`(?i)unapproved channel|illegal api invocation`)

// promptRefused says a vendor answered with that refusal: the agent's
// prompt, not the account, is at fault.
func promptRefused(status int, body []byte) bool {
	return status >= 400 && refusedWords.Match(body)
}

// shapeWords are how a vendor says it can't read the request's shape — an
// item, field or parameter it doesn't know, which another vendor's API may
// take: xAI's 422 "Failed to deserialize the JSON body …: unknown item type"
// (#350), OpenAI's "Unknown parameter", and a limit of its own on a tool's
// JSON schema: OpenCode Go's 400 "JSON schema exceeds the maximum nesting
// depth of 10 levels" on muse-spark-1.3-contributor for Claude Code's MCP
// tools (#1870), which another member's model may take as they are; and a
// custom tool turned away (customToolRefusal, DeepSeek's "Unsupported
// custom tool: 'exec'", #2656) where no translation took it in. A
// request missing what every API requires ("field required") or too long
// for the model isn't one. Google's refusal of a function declaration's
// schema, which points at it (GenerateContentRequest.tools[0].
// function_declarations[17].parameters.properties[batch].items: missing
// field, plugins#375), is the same kind as OpenCode Go's: its subset of
// JSON Schema is its own, and another vendor's model took the reporter's
// same tools. A relay passing Google's own Gemini on says the same in the
// words Google gave it, camel-case and with no path: "Unable to submit
// request because `tool` functionDeclaration `parameters.response` schema
// specified other fields alongside any_of. When using any_of, it must be
// the only field set." (#5374, worksyang) — a description beside an
// anyOf, which Google's Schema takes alone, the reporter's same tools
// another member took.
var shapeWords = regexp.MustCompile(`(?i)failed to deserialize|unknown (item |content |input )?(type|variant|field|parameter)|unknown_parameter|unrecognized (request argument|field|parameter)|extra (inputs|fields) are not permitted|additional properties are not allowed|schema exceeds the maximum (nesting )?depth|schema[^"]{0,60}\bnesting depth\b|function_declarations\[\d+\]\.parameters\b|schema specified other fields alongside any_?of`)

// unsaidWords are Command Code's Provider API turning a request away with
// nothing but a trace id: {"message":"invalid request error trace_id:
// <hex>","type":"invalid_request_error"}, a 400 (a 422 before), no code,
// no param (#2313, lqisgod). What it turns away is the request's size:
// the reporter's A/B on one agent conversation (~748K tokens of history
// and tools) answered 200 with max_completion_tokens 64000 and this 400
// with 384000, the model's own listed max output, so the history and the
// output asked for together are over a limit below the 1M context it
// lists; its tracker has more of the kind (CommandCodeAI/command-code#960,
// #976). The key and the account are fine, and another member, whose
// model has room for it, may well take the same request: it is asked,
// once, as for shapeWords, and Command Code doesn't rest. Its overflow
// that says so names the model's context length before the trace id, and
// isn't one. The message may come after a provider's name ("Command Code:
// "), as magpie tells it on.
var unsaidWords = regexp.MustCompile(`(?i)(^|"|\\"|: )invalid request error trace_id: [0-9a-f]{16,}(\\?"|\s*$)`)

// shapeRefused says a request failed over its shape alone: the next
// member is asked, and this one doesn't rest, as nothing is wrong with it.
// An upstream's word that the request is served on another API than the
// one asked (wrongEndpoint) is one too, once it reaches the group: the
// member has tried every API it has a URL for (forwardTranslated), and
// another member, on another API or another vendor, may serve it (#2670:
// a relay with a Chat URL alone, refusing tools with reasoning there).
// A relay's own word that the provider behind it turned the request away
// (upstreamFailedWords: "sub2api: Upstream request failed", type
// upstream_error) is the relay's failure, not this seat's and not the
// request's — the words the Codex compaction already reads (#866, #1511) —
// and another member may answer (#4632: the group's second member never
// ran, and every turn died on the first seat).
// GLM's word for a picture it can't read (zhipuImageRefusal, #4732) is
// one: another vendor takes the webp as it is.
// A model's demand for an earlier turn's reasoning the history doesn't
// have (reasoningBackRefused: Beam's "Missing required parameter:
// 'messages[1].reasoning_content'", EDwinpzy) is one too: the client's
// request is whole, and a group of Beam then another model stopped at
// Beam before.
// Google's word for a thought signature it didn't give (signatureRefused:
// "Corrupted thought signature.", one Vertex AI gave replayed to AI Studio,
// mimi001234) is one: the member that gave it may take it.
// One that also says the account is out of quota or credit (quotaWords,
// creditWords: "请充值") is that, and rests it (#4095).
func shapeRefused(status int, body []byte) bool {
	// the words are matched decoded, too (#1669)
	body = wordsOf(body)
	return (status == 400 || status == 422) && (shapeWords.Match(body) || unsaidWords.Match(body) || customToolRefusal.Match(body) || zhipuImageRefusal.Match(body) || wrongEndpoint(status, body) || upstreamFailedWords.Match(body) || reasoningBackRefused(body) || signatureRefused.Match(body)) &&
		!quotaWords.Match(body) && !creditWords.Match(body) && !unservedWords.Match(body) && !refusedWords.Match(body)
}

// regionWords are how a vendor says it doesn't serve where the request
// comes from: Google's 400 FAILED_PRECONDITION "User location is not
// supported for the API use." (Gemini, Antigravity, Code Assist; #1621),
// and OpenAI's "unsupported_country_region_territory", which a relay may
// pass on as a 400. It is about the address the request leaves from, not
// the request, the model or the account: another member, going another
// way, may answer, and none rests for it (restAfterMarked, #5182). Google
// decides it per connection, so the connection it came on is not used
// again (regionFresh).
// A request's own field named location ("location field is not
// supported") isn't one.
var regionWords = regexp.MustCompile(`(?i)\buser location is not supported for the api use\b|unsupported_country_region_territory|country, region,? or territory not supported`)

// regionRefused says the vendor refused the request for where it came
// from, with regionWords.
func regionRefused(status int, body []byte) bool {
	return status >= 400 && regionWords.Match(body)
}

// googleRegionWords is Google's wording of the refusal (Gemini, Code
// Assist, Antigravity), which names the vendor in regionNote.
var googleRegionWords = regexp.MustCompile(`(?i)\buser location is not supported for the api use\b`)

// regionNote is what the agent is told beside a refusal of where the
// request came from (regionRefused), or "" for any other reply: the
// vendor's restriction is on the model asked, from where the request
// leaves, so its other models may answer on the same account
// (taoxiaoxin, #5182: Antigravity's gemini-3.8-flash refused, its Claude
// models answered), and no account rests for it.
func regionNote(model string, status int, body []byte) string {
	words := wordsOf(body)
	if !regionRefused(status, words) {
		return ""
	}
	who := "the vendor"
	if googleRegionWords.Match(words) {
		who = "Google"
	}
	return fmt.Sprintf("%s doesn't serve %s to requests from the region this one left from: a regional restriction on the model, not on the account, so no account rests for it and the provider's other models may still answer. Use another provider for %s, or give the provider a proxy in a region %s serves (its Proxy setting)", who, model, model, who)
}

// explained is p's words for a failed reply b, as the agent is told them
// (Provider.Explain), with regionNote for a refusal of where it came from.
func explained(p provider.Provider, model string, res *http.Response, b []byte) string {
	msg := p.Explain(p.Name+": "+provider.APIError(b, res.Status), res.StatusCode, b)
	if note := regionNote(model, res.StatusCode, b); note != "" {
		msg += " — " + note
	}
	return msg
}

// protectionWords are how the ChatGPT backend answers a long Codex
// conversation it won't take as it is: 502 "response protection is
// unavailable" (vs on Discord, 0.1.1108). It comes back the same for the
// same history on every account and model the backend serves, and the
// history as plain text is answered, so it is about the request, not the
// account: asked again there, it only drains the accounts' allowances.
var protectionWords = regexp.MustCompile(`(?i)response protection is unavailable`)

// protectionRefused says the vendor turned the request's content away
// with protectionWords: the account is not at fault, and none of its
// provider's other accounts or models is asked the same.
func protectionRefused(status int, body []byte) bool {
	return status >= 400 && protectionWords.Match(body)
}

// zdrCoded is Command Code's error code for a model no Zero Data
// Retention upstream serves, as the error's code or type, asked with
// x-cmd-zdr: 1 (its plugin's switch, magpie-community/plugins#246): the
// Provider API's 422 {"error":{"message":"This model has no
// zero-data-retention upstream. Remove the x-cmd-zdr header or choose a
// different model.","type":"invalid_request_error","code":
// "cmd_zdr_no_providers"}}, and the 400 the plugin makes of Go's error
// line, {"error":{…,"type":"cmd_zdr_no_providers","code":400}}. Matched
// where the error names its code, not in its words: "…a different
// model.","type":"invalid_request_error" reads as a model unserved
// (unservedWords) otherwise.
var zdrCoded = regexp.MustCompile(`\\?"(type|code)\\?"\s*:\s*\\?"cmd_zdr_no_providers\\?"`)

// zdrRefused says Command Code turned the request away for its Zero Data
// Retention setting: a choice the user made for every request to the
// provider, not the model or the account failing. Nothing rests for it,
// so the model answers again as soon as the switch is off; the same
// model at the same provider, sent the same header, isn't asked again;
// another member may be.
func zdrRefused(status int, body []byte) bool {
	return (status == 400 || status == 422) && zdrCoded.Match(body)
}

// zdrHeader carries, from keepRetry to the routing loop and restAfter,
// that a vendor's error was zdrRefused: the error the agent gets keeps
// Command Code's words but, in Anthropic's shape or for the 400 whose
// code is a number, not its code.
const zdrHeader = "X-Magpie-ZDR-Refused"

// notMates is, of the candidates left, those not c's model at c's
// provider: its other accounts are sent the same Zero Data Retention
// header, and refuse the same.
func notMates(left []candidate, c candidate) []candidate {
	var out []candidate
	for _, x := range left {
		if x.p.ID != c.p.ID || x.model != c.model {
			out = append(out, x)
		}
	}
	return out
}

// elsewhere is, of the candidates left, those not at c's provider, whose
// every account and model the same request reaches the same backend
// through.
func elsewhere(left []candidate, c candidate) []candidate {
	var out []candidate
	for _, x := range left {
		if x.p.ID != c.p.ID {
			out = append(out, x)
		}
	}
	return out
}

// riskRefused says c's vendor refused it for the account's safety — its
// risk control restricted or blocked the account (failRestricted:
// WorkBuddy's 11140, ZCode's 3012, or any plugin's refusal ending in
// provider.WBRestrictedHint or provider.ZCodeRestrictedHint) — rather
// than for quota or a passing fault. A vendor's risk control reads the
// request as well as the account, so the same request sent on to another
// of its accounts trips it there too. Another computer's magpie that
// says so (remoteModelRest) speaks for one account of its own, and its
// other models may be other vendors: it walls off nothing here. Nor does
// Google's suspension of an account for its terms (googleSuspended): it
// says the service was disabled in this account before the request came,
// a ruling on the account, not on what was sent, so the provider's other
// accounts are asked (bestony on Discord).
func riskRefused(c candidate, status int, header http.Header, body []byte) bool {
	return failureOf(c, status, body) == failRestricted && !remoteModelRest(c, failRestricted, header) && !googleSuspended(c.p, carriedStatus(status, body), body)
}

// elsewhereWeighed is elsewhere on a list of candidates beside their
// weights (roomy's left out as too small), the two kept paired: weights
// that don't pair with the list are kept as they are.
func elsewhereWeighed(left []candidate, w []Weighed, c candidate) ([]candidate, []Weighed) {
	paired := len(w) == len(left)
	var out []candidate
	var outW []Weighed
	for k, x := range left {
		if x.p.ID == c.p.ID {
			continue
		}
		out = append(out, x)
		if paired {
			outW = append(outW, w[k])
		}
	}
	if !paired {
		outW = w
	}
	return out, outW
}

// retryable says whether another provider may do better with a request
// that failed this way: the vendor was busy, out of quota or failing, or
// this key or provider can't serve it — not the request itself at fault.
func retryable(status int, body []byte) bool {
	raw := body
	// the words are matched decoded, too (#1669)
	body = wordsOf(body)
	switch {
	case overflowed(status, body):
		// a conversation too long for the model is as long on every
		// account of it: the agent is told, and compacts
		return false
	case status == 401, status == 402, status == 403, status == 404, status == 408, status == 429, status >= 500:
		return true
	case modelRetired(status, body):
		// another provider of the model may still serve it
		return true
	case status >= 400 && provider.EdgeBlocked(body):
		// the vendor's firewall blocked this address (Alibaba Cloud's 405
		// in front of zcode.z.ai): another provider goes another way
		return true
	case status >= 400 && status < 500 && failure(status, raw) == failCredit:
		// out of credit, as failure reads it, whatever the status: another
		// account may have a balance. SuperMagic's 400 "您的可用积分已不足…
		// 通过「充值」通道补充积分" read as credit, but no word of
		// quotaWords' is in it, so a group never asked the account with
		// 6,467 points and the empty one was never rested (#4095,
		// mycf_711)
		return true
	case status == 400, status == 422:
		return quotaWords.Match(body) || unservedWords.Match(body) || refusedWords.Match(body) || shapeWords.Match(body) || unsaidWords.Match(body) || regionWords.Match(body) || zhipuImageRefusal.Match(body) || wrongEndpoint(status, body) ||
			// a relay's own word that the provider behind it turned the
			// request away (#4632): another member may answer
			upstreamFailedWords.Match(body) ||
			// a model's demand for an earlier turn's reasoning (EDwinpzy):
			// another member's model may not make it
			reasoningBackRefused(body)
	}
	return false
}

// lateRests says whether a reply that broke off after it began, with this
// error, was the vendor failing (#733): unreachable, overloaded, out of
// quota. A conversation grown too long for the model, or a request the
// vendor turned away as it reads, would fail the same at the next asked:
// nobody rests for it. Nor does an account rest when a model generated
// empty output (#1221), nor for any error in magpie's own words, a LAN
// peer's loop guard among them (magpieSaid).
func lateRests(msg string) bool {
	return !tooLong(http.StatusBadRequest, msg) && !refusedWords.MatchString(msg) && !protectionWords.MatchString(msg) && !magpieSaid(msg)
}

// unsaidMargin is how far past a model's window a request's estimate
// must go to be taken as too long for it: estimate counts a token as four
// bytes, which most text runs under (Devin counted 173,954 tokens of one
// estimated at 151,851), so one past it by a fifth hardly fits.
const unsaidMargin = 1.2

// lengthMargin is how far into a model's window a request must be for a
// refusal that speaks of length, without naming what is long, to be taken
// as too long for it (#4420, MYCF711): a group member answered 400
// "内容太长了，请精简输入或开启新对话" at ~730K on a model it declares at
// 1M, its own ceiling lower than the window it declares, so the request
// never came near unsaidMargin. What the vendor's message says decides:
// length words (lengthWords) and nothing of a limit of another kind; the
// request's size only rules out the other things such words are said of,
// a field or a picture, which a request under half the window more likely
// is. tooLongRe's wordings are the fallback that holds at any size.
const lengthMargin = 0.5

// lengthWords say the request is too long, or to make it shorter or start
// over, without naming what overflowed.
var lengthWords = regexp.MustCompile(`(?i)太长|过长|超长|精简|缩短|删减|(开启|新建|开始|重新开启)(一个)?新的?(对话|会话|聊天)|too (long|large|big)|shorten|reduce (the )?(length|size)|start a new (conversation|chat|session)`)

// partWords name a part of the request whose length isn't the
// conversation's: a picture, a file, the reply, a field, a name.
var partWords = regexp.MustCompile(`(?i)图片|图像|文件|附件|输出|回复|名称|字段|参数|标题|image|picture|file|attachment|output|max_tokens|max_completion|completion|title|description`)

// saysLength says a request error's message speaks of the request's length
// and of nothing else: not a pace, a quota, a part of the request.
func saysLength(status int, body []byte) bool {
	if status != 400 && status != 422 {
		return false
	}
	said := vendorMessage(body)
	return lengthWords.MatchString(said) && !partWords.MatchString(said) && !paceWords.MatchString(said)
}

// tooLongUnsaid says a failure that doesn't say why was the request being too
// long for the model, as a few vendors put it (unsaidOverflows): Devin's
// 502 "capacity issues", WorkBuddy's 400 "Invalid request parameters",
// Qoder's 400 "Error in upstream response" — each for a conversation well
// past the model's window, and answered at a size it holds (b3ab3e42e) —
// and Factory's 413 "Request Entity Too Large" for one whose text alone is
// past it (#711). Retried, or asked of another account of the model, it
// fails the same, and the agent, told it as a vendor's error, never
// compacts. The message it is told instead says the prompt is too long.
// Not for a rate limit, an account's trouble, a model it doesn't serve, a
// region it doesn't serve, or one already saying it overflowed.
//
// Every other provider's failure reaches the agent as the vendor said it,
// its status, code and body, however long the request: a custom
// Responses relay's plain 502, and the 400 or 413 any relay, OpenAI or
// Anthropic may give for something else entirely, aren't magpie's to
// read as an overflow (#5486, NEKO-CwC: ne9ko9's relay had a 502 at 385K
// told to Codex as more than gpt-6-astra's 272K, and Codex ended the
// turn). The one exception is a request error whose own words say the
// request is too long (saysLength), from any vendor: the vendor named
// the cause, and lengthMargin of the window rules out the other things
// such words are said of. Nor is a failure that is no answer at all
// (transportFailed): a dial or read that timed out, a connection reset, a
// stream cut short, each in magpie's own words. held is the most the
// model was seen to answer for this provider (Server.heldOf), and the
// window is never less than it, nor than the most the provider's list
// says the model may be asked for (windowMost).
func tooLongUnsaid(p provider.Provider, model string, tokens, held, status int, body []byte) (string, bool) {
	unsaid := unsaidOverflows(p, status)
	if !unsaid && status != 400 && status != 422 {
		return "", false
	}
	raw := body
	// the words are matched decoded, too (#1669)
	body = wordsOf(body)
	if transportFailed(raw) || transportFailed(body) {
		return "", false
	}
	length := saysLength(status, raw)
	if !unsaid && !length {
		return "", false
	}
	if overflowed(status, body) || failure(status, body) != failOther || quotaWords.Match(body) ||
		unservedWords.Match(body) || refusedWords.Match(body) || provider.EdgeBlocked(body) || regionRefused(status, body) || zdrRefused(status, body) {
		return "", false
	}
	window := windowOf(p, model)
	if window <= 0 {
		return "", false
	}
	window = max(window, windowMost(p, model), held)
	margin := unsaidMargin
	if length {
		margin = lengthMargin
	}
	if float64(tokens) < float64(window)*margin {
		return "", false
	}
	msg := fmt.Sprintf("prompt is too long: about %d tokens, more than %s's context window of %d", tokens, model, window)
	if float64(tokens) < float64(window) {
		msg = fmt.Sprintf("prompt is too long: about %d tokens, more than %s takes here, though its context window is %d", tokens, model, window)
	}
	said := strings.TrimSpace(vendorMessage(body))
	if len(said) > 200 {
		said = said[:200] + "…"
	}
	// the vendor's words, unless they'd read as a limit of another kind
	if full := fmt.Sprintf("%s (%s answered %d: %s)", msg, p.Name, status, said); said != "" && tooLong(400, full) {
		return full, true
	}
	return fmt.Sprintf("%s (%s answered %d)", msg, p.Name, status), true
}

// unsaidStatus are the statuses a vendor was seen to answer a request
// past its model's window with, saying nothing of length, by the id of
// the plugin that serves it now (each is retired as a built-in, so only
// its plugin serves it): Devin's 502 "We are currently experiencing
// capacity issues" for about 300K tokens on swe-1.7-lightning (202,752),
// Qoder's 400 "Error in upstream response" for 1.8M on qmodel (1M) and
// WorkBuddy's 400 "Invalid request parameters" on glm-5.1, each answered
// at a size it holds (b3ab3e42e, v0.1.735), and Factory's 413 for a
// request whose text alone is past the window (#711, v0.1.808). Keyed by
// the plugin, never by the provider's id, which a user's own provider
// may share.
var unsaidStatus = map[string][]int{
	"devin":        {500, 502, 503, 504},
	"qoder":        {400, 413, 422},
	"qoder-cn":     {400, 413, 422},
	"workbuddy":    {400, 413, 422},
	"workbuddy-ai": {400, 413, 422},
	"factory":      {413},
}

// unsaidOverflows says p's vendor answers a request past its model's
// window with status, saying nothing of length (unsaidStatus).
func unsaidOverflows(p provider.Provider, status int) bool {
	return slices.Contains(unsaidStatus[p.PluginProvider()], status)
}

// transportWords are a failure that never reached a vendor's answer, as Go,
// Bun or Windows say it, or magpie's own words for a reply that broke off:
// a request that met one may well fit, and is asked again.
var transportWords = regexp.MustCompile(`(?i)dial (tcp|udp)|connectex|wsarecv|wsasend|i/o timeout|TLS handshake timeout|context deadline exceeded|Client\.Timeout|timeout awaiting response headers|connection (was )?(reset|refused|aborted|closed)|forcibly closed|broken pipe|unexpected EOF|no such host|network is unreachable|no route to host|server closed idle connection|Unable to connect|ConnectionRefused|ConnectionClosed|FailedToOpenSocket|ETIMEDOUT|ECONNRESET|ECONNREFUSED|ECONNABORTED|ENOTFOUND|EAI_AGAIN|ENETUNREACH|EHOSTUNREACH|EPIPE`)

// transportFailed says an error body is a transport failure rather than a
// vendor's answer (transportWords), or one magpie made of a reply that
// broke off or said nothing.
func transportFailed(body []byte) bool {
	return transportWords.Match(body) || bytes.Contains(body, []byte(errEndedShort.Error())) ||
		bytes.Contains(body, []byte(didNotStream)) || magpieSaid(string(body))
}

// noteHeld keeps n as the most prompt tokens the provider's model has
// answered, when it is more than the most kept: a model that answered a
// prompt of n holds one of n, whatever its window is said to be (ne9ko9
// on Discord: gpt-6-astra, said at 272K, answered 454K and 474K).
func (s *Server) noteHeld(id, model string, n int) {
	if n <= 0 {
		return
	}
	key := id + "\x00" + model
	for {
		cur, ok := s.held.Load(key)
		if !ok {
			if _, loaded := s.held.LoadOrStore(key, n); !loaded {
				return
			}
			continue
		}
		if cur.(int) >= n || s.held.CompareAndSwap(key, cur, n) {
			return
		}
	}
}

// heldOf is the most prompt tokens the provider's model has answered since
// magpie started (noteHeld), 0 when none.
func (s *Server) heldOf(id, model string) int {
	if v, ok := s.held.Load(id + "\x00" + model); ok {
		return v.(int)
	}
	return 0
}

// windowMost is the most the provider's list says its model may be asked
// for (Entry.MaxContext), 0 when it says none: a Codex entry's
// max_context_window of 872K above a 272K context_window, which Codex is
// handed and may work a conversation up to.
func windowMost(p provider.Provider, model string) int {
	cat := provider.Served()
	for i := range cat {
		e := &cat[i]
		if e.Group == "" && e.Provider.ID == p.ID && (e.Model == model || e.ID == model) {
			return e.MaxContext
		}
	}
	return 0
}

// windowOf is the tokens a provider's model takes, where known: the
// user's for the model, else its catalog entry's (Provider.WindowOf, where
// the one set for all the provider's models is no larger than the model's
// own, #2112), else the one set for all of them.
func windowOf(p provider.Provider, model string) int {
	return windowIn(provider.Served, p, model)
}

// windowIn is windowOf with the catalog read from served, for a caller
// that weighs several models to read it once.
func windowIn(served func() []provider.Entry, p provider.Provider, model string) int {
	if n := p.Contexts[model]; n > 0 {
		return n
	}
	cat := served()
	for i := range cat {
		e := &cat[i]
		if e.Group == "" && e.Provider.ID == p.ID && (e.Model == model || e.ID == model) && e.Context > 0 {
			return e.Context
		}
	}
	return p.ContextOf(model)
}

const (
	// lastRetries is how many times the last one left is tried again after
	// a failure that passes — a busy vendor, a dropped connection.
	lastRetries = 2
	// rateRetries is as many for a rate limit, which takes longer to
	// clear than a busy moment: pauses of 1, 2 and 4s, or what Retry-After
	// says, under half a minute in all
	rateRetries = 3
)

// longestPause is the longest the vendor's Retry-After is waited for by
// the same-one retries; longer, and a plain provider or a group waits it
// out within its patience (replan), else the agent gets the error.
var longestPause = 8 * time.Second

// retryPause is the first pause before the last one left is tried again;
// each pause after is twice the one before.
var retryPause = time.Second

// passing says whether a failure is one that may be gone a moment later,
// and how long to wait before trying the same one again, the again'th time.
// An upstream that answered a stream request with a whole reply
// (didNotStream) isn't one: it fails the same every time it is asked.
func passing(status int, header http.Header, body []byte, again int) (time.Duration, bool) {
	wait := retryPause << again
	if d := retryAfter(header, time.Now()); d > 0 {
		wait = d
	}
	switch {
	case wait > longestPause:
		return 0, false
	case busyStatus(status):
		if bytes.Contains(body, []byte(didNotStream)) || bytes.Contains(body, []byte(cutVoidWords)) {
			// a void cut at its length fails the same every ask: the
			// budget went to reasoning, and mostly would again — the same
			// one asked again would burn it twice over before the error
			// (#2972). A group's next member still is.
			return 0, false
		}
		return wait, again < lastRetries
	case status == 429:
		// a relay's 429 often says nothing of when (#503: "负载已饱和，请稍
		// 后再试"); a plan used up or no money left won't clear in seconds,
		// nor an account's token wall (walled)
		return wait, again < rateRetries && failure(status, body) == failRate && !creditWords.Match(wordsOf(body)) && !walled(body)
	case regionRefused(status, wordsOf(body)):
		// refused at the edge the connection reached, which a new
		// connection a moment later may not be (#5182: answered 12s on);
		// the try goes out on a new one (regionFresh)
		return wait, again < lastRetries
	}
	return 0, false
}

// wallCodes are the error codes a vendor gives a 429 for an account's
// own token throughput used up, a bucket that refills slowly over minutes
// (plugins#512, cycocyco): SenseTime Raccoon's user_model_rate_limited,
// "您的模型请求频率超限，请稍后重试", with no Retry-After. It reads as a
// rate limit and rests as one, a minute and longer each time it comes
// back (rateRest), but no moment clears it: the same-one retries and a
// patience's rounds sent the whole request again eight times in 38s, each
// one drawing on the bucket it waited for. The next account is asked, or
// the agent has the error at once.
var wallCodes = map[string]bool{"user_model_rate_limited": true}

// zcodeRateWall is ZCode's account-wide 429, the same bucket as
// wallCodes' (#5406, erlinerd): rested and retried, the 429 came back, and
// two further accounts sat there while magpie kept asking (yetone on two
// Discord reports, v0.1.3216). It is matched by the ZCode plugin's own
// naming of the refusal, the vendor's three-part label `[1302][<its
// words>][<its request id>]`, its words whole — the shape the two log
// lines of #5406 show — never by the bare code: Zhipu's own API answers
// 1302 for a concurrency or minute limit ("并发数过高", "您的账户已达到速率限制"
// with no label), which a moment does clear, and a GLM key asked again
// in place stays so. The words and the id together are the label's own,
// not words anywhere in the body a prompt may quote.
var zcodeRateWall = regexp.MustCompile(`\[1302\]\[您的账户已达到速率限制[^\]]*\]\[[0-9a-fA-F]{16,}\]`)

// walled says whether body is a refusal whose error.code is one of
// wallCodes: the code where the vendor puts it, not the words anywhere in
// the body (a prompt or a message quoting it).
func walled(body []byte) bool {
	var e struct {
		Error struct {
			Code json.RawMessage `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		return false
	}
	var code string
	if json.Unmarshal(e.Error.Code, &code) == nil && wallCodes[code] {
		return true
	}
	return zcodeRateWall.Match(wordsOf(body))
}

// withRoom is, of the candidates left, those another member than c's may
// answer a request of about tokens that c's model found too long: not an
// account of c's model at c's provider, which holds it no better, nor one
// whose window it is known to be past.
func withRoom(left []candidate, c candidate, tokens int) []candidate {
	var out []candidate
	for _, x := range left {
		if x.p.ID == c.p.ID && x.model == c.model {
			continue
		}
		if !holds(x, tokens) {
			continue
		}
		out = append(out, x)
	}
	return out
}

// holds is whether c's model may hold a request of about tokens: its
// window isn't known, or is larger.
func holds(c candidate, tokens int) bool {
	w := windowOf(c.p, c.model)
	return w <= 0 || tokens < w
}

// roomy leaves out, before any is asked, the candidates whose model is
// known to take less than the request (dgqyushen on Discord: OpenCode
// Zen's free DeepSeek V4.1 Flash, 200K, first in a group with a 1M
// subscription of it second). Asked, such a member only refuses it, a
// request spent against its limits, and a refusal that doesn't say why,
// for a request short of tooLongUnsaid's margin past the window, reached
// the agent as the vendor's error while the next member had room. The
// request's size is estimate's, which mostly counts low (four bytes a
// token), so a member is passed over only when the request is past its
// window by that count; one nearer may still be refused and falls over
// (withRoom). A model whose window isn't known is kept, a
// request that doesn't parse leaves every one, and so does one no
// candidate holds: the first is asked, and the agent told it overflowed,
// to compact.
//
// Those left out are handed back (small, their rows in smallW when the
// order has one each, with Window set to the window: the trace lists
// them as left out, so the Routing page keeps their rows and says why,
// #3749), kept for the last resort: when every candidate
// with room is out of its allowance, a fallback too small is asked all
// the same (Kai on Discord: both Claude accounts at their limit until
// the next day, the Claude provider's Fallback a Codex model of 272K
// under a longer conversation, left out without a word, and Claude Code
// handed the 429). Its overflow tells the agent to compact, after which
// the fallback holds it; the 429 told it only to wait for hours.
func roomy(cs []candidate, pl planned, from provider.Protocol, body []byte) (out []candidate, _ planned, small []candidate, smallW []Weighed) {
	if len(cs) < 2 {
		return cs, pl, nil, nil
	}
	served := sync.OnceValue(provider.Served) // the catalog, read once
	least, windows := 0, map[string]int{}
	key := func(c candidate) string { return c.p.ID + "\x00" + c.model }
	for _, c := range cs {
		if _, ok := windows[key(c)]; ok {
			continue
		}
		w := windowIn(served, c.p, c.model)
		windows[key(c)] = w
		if w > 0 && (least == 0 || w < least) {
			least = w
		}
	}
	// one model has nothing to choose between, and a body shorter than
	// the least window can't estimate past it: neither is parsed
	if len(windows) < 2 || least == 0 || len(body)/4 < least {
		return cs, pl, nil, nil
	}
	req, err := parse(from, body)
	if err != nil {
		return cs, pl, nil, nil
	}
	tokens := estimate(req)
	var order []Weighed
	parallel := len(pl.order) == len(cs)
	for i, c := range cs {
		if w := windows[key(c)]; w > 0 && tokens >= w {
			small = append(small, c)
			if parallel {
				x := pl.order[i]
				x.Window = w
				smallW = append(smallW, x)
			}
			continue
		}
		out = append(out, c)
		if parallel {
			order = append(order, pl.order[i])
		}
	}
	if len(out) == 0 || len(out) == len(cs) {
		return cs, pl, nil, nil
	}
	if parallel {
		pl.order = order
	}
	return out, pl, small, smallW
}

// matesFirst puts first, of the candidates left, the other keys or
// accounts of the member c is of — its model, at its effort — that aren't
// resting: what one account's safety filter refused, another may answer
// (one verified for the vendor's trusted access), the same model before
// the group's next (#248). A conversation kept on the account that
// answered it last has that one alone first, and its member's other
// accounts where the member is in the group: after Sonnet, first in it,
// when Codex's gpt-6.1-sol refused.
func matesFirst(left []candidate, c candidate) {
	mate := func(x candidate) bool {
		if x.p.ID != c.p.ID || x.model != c.model || x.effort != c.effort || x.who() == c.who() {
			return false
		}
		_, resting := restOf(x.restKey())
		return !resting
	}
	var mates, others []candidate
	for _, x := range left {
		if mate(x) {
			mates = append(mates, x)
		} else {
			others = append(others, x)
		}
	}
	copy(left, append(mates, others...))
}

// retellMost is the size, in tokens, of a refused conversation past which
// another Claude subscription account isn't told it again: each is a new
// Claude Code that writes the whole of it to that account's cache.
const retellMost = 100_000

// claudeAccount is whether c is a Claude subscription account, served
// through Claude Code (serveClaudeSubscription).
func (c candidate) claudeAccount() bool {
	return c.p.Account != nil && c.p.Account.Agent == "claude"
}

// tooBigToRetell is whether a conversation is too big to be told again to
// another Claude subscription account after one refused it (retellMost),
// and its size in tokens as estimate counts it.
func tooBigToRetell(from provider.Protocol, body []byte) (int, bool) {
	req, err := parse(from, body)
	if err != nil {
		return 0, false
	}
	n := estimate(req)
	return n, n >= retellMost
}

// otherThanClaudeAccounts is left without its Claude subscription accounts.
func otherThanClaudeAccounts(left []candidate) []candidate {
	var kept []candidate
	for _, x := range left {
		if !x.claudeAccount() {
			kept = append(kept, x)
		}
	}
	return kept
}

// matesLast puts last, of the candidates left, the other keys or accounts
// of the member c is of: what turns one of them away is in the request, so
// it turns them all away the same, and the group is better spent on a
// member that answers (#666). Antigravity refuses Claude Code's and its
// Agent SDK's system prompt whichever account is asked.
func matesLast(left []candidate, c candidate) {
	mate := func(x candidate) bool {
		if x.p.ID != c.p.ID || x.model != c.model || x.effort != c.effort || x.who() == c.who() {
			return false
		}
		_, resting := restOf(x.restKey())
		return !resting
	}
	var mates, others []candidate
	for _, x := range left {
		if mate(x) {
			mates = append(mates, x)
		} else {
			others = append(others, x)
		}
	}
	copy(left, append(others, mates...))
}

// holdWriter keeps an error reply back while another provider may still
// answer: headers and body wait until release, or are dropped for the next
// try. Anything else goes straight through — but for a stream, only once
// its first content comes: an error before that is a failure another may
// answer, as an error status is. So is a safety refusal with nothing said
// before it (#248), and a reply that isn't streamed is held whole to read
// for one.
type holdWriter struct {
	w       http.ResponseWriter
	hold    bool
	header  http.Header
	status  int
	passing bool
	held    bytes.Buffer

	stream     bool      // a stream held until its first content
	since      time.Time // when it began
	scanned    int       // how much of held has been read as events
	failure    int       // the status the stream's error stands for
	failMsg    string
	sharedPool bool // an OpenRouter upstream pool rejected this attempt

	// turnedAway: the account is asked with the system prompt Antigravity
	// answers with a 429 whatever quota it has left — Claude Code's, its
	// Agent SDK's — so the account isn't at fault and doesn't rest (#666)
	turnedAway bool
	// turnedAwayHint says why, for that system prompt (antigravityHint)
	turnedAwayHint string

	// refused: the vendor's safety filter ended the reply before any of it
	// was said — Anthropic's stop_reason "refusal", OpenAI's content_filter
	// — which another account or model may answer (#248)
	refused bool
	// refusedAfter: the same filter ended a reply that had already begun,
	// which the scan of a held stream never sees, as the reply goes
	// through as it comes. A refusal isn't a failure of the account, so
	// nobody rests for it, whatever its timing (#248)
	refusedAfter bool
	// calls: a tool call of some kind has gone by in the reply, so a
	// refusal after it is not handed over (handsOff): the call the agent
	// has would sit beside the next member's, broken or doubled
	calls bool
	// after is what came of the reply once it began, for a refusal in it
	// to be read event by event
	after []byte
	whole bool // a reply that isn't streamed, held whole until release
	// wholeUnsure: held whole only as the agent has a stream's headers,
	// under a type that says neither JSON nor a stream: the reply's first
	// bytes say whether it streams after all (Write)
	wholeUnsure bool

	// buffered: the vendor said it holds the reply back for safety checks,
	// which may end in a refusal: held longer (holdBuffered)
	buffered bool
	// thinking: the reply has reasoned but said nothing yet, which a
	// refusal may still end: held longer (holdThinking)
	thinking bool
	// thinkingShown: the model's vendor never refuses after reasoning
	// (refusesAfterThinking), or the member has gone on from its thinking
	// to an answer enough times running (holdsThinking), so its reasoning
	// goes through as it comes
	thinkingShown bool
	// thoughtThrough: the try went through on its reasoning alone
	// (thinkingShown) while the request could still have asked again, so
	// an empty reply is asked again in place as often (emptyRetries)
	thoughtThrough bool

	ended bool   // the stream's last event was written: the reply is whole
	tail  []byte // the end of the last write, for a marker split across two
	// ctx is the agent's request: a stream's last event written once it
	// has gone reached nobody, and doesn't make the reply whole (#751)
	ctx context.Context

	// alive is the request's, shared by its tries: a stream held for its
	// reasoning keeps the agent from its idle timeout with SSE comments
	// meanwhile (#751)
	alive *keptAlive
	// notes are the headers magpie says of the try (noteMember), set
	// under mu: all keepAlive sends of the try's before it has its status,
	// as until then the try is still writing header, with no lock
	notes http.Header

	first firstToken // when its first content and text came (#196)

	// stop ends the try's request to the vendor: a stream whose error
	// came before any content has failed, and one that kept its
	// connection open after it — a 429 said as an event — kept the agent
	// waiting with nothing sent, the next account never asked
	stop func()

	// streams: the agent asked for a stream, which watch keeps alive
	// while the vendor says nothing (#947)
	streams bool
	// heard is when the vendor last gave the try something to write, and
	// wrote when the agent was last written to; atLine says that was a
	// whole line, after which an SSE comment may go
	heard, wrote time.Time
	atLine       bool

	// mu keeps a try's writes apart from watch, which ends a try that is
	// slow to start and keeps a quiet one's agent alive from its own
	// goroutine
	mu sync.Mutex
	// firstWait is how long a try may take to its first content before
	// the next member is asked (Group.FirstToken), 0 for as long as it
	// takes; slow says it took longer, and was let go with nothing sent
	firstWait time.Duration
	slow      bool
	// budgeted says firstWait was cut to what was left of the walk's
	// total time (groupBudget), so its running out is the budget's end,
	// not this member being slow (#5113)
	budgeted bool

	// loop reads a streamed reply for a loop it won't leave (#1359), nil
	// when Settings' NoLoopGuard is on or the agent asked for no stream;
	// looped is what the agent was told once one was found, the reply
	// ended there
	loop   *loopGuard
	looped string
	// reask, when the try's reply can be asked again in place
	// (streamTranslated), is told of a loop first, and says whether the
	// reply is asked again instead of ended: the guard then reads the
	// next try's afresh
	reask func(loopTrip) bool
	// reasked is how many times reask asked the reply again in place,
	// tries of the request its route doesn't list (#2436)
	reasked int
	// reloop: a reply passed through, whose reasoning loops before it
	// said anything, may be let go for the request to ask it again
	// (loopReasks, #2436); relooped says it was
	reloop, relooped bool

	// wantWhole: a reply wanted only whole, a compaction's summary (#2274): its
	// stream is held to its end, never let through at its first content,
	// so an error or a cut anywhere in it is a failure the next candidate
	// answers, and its account rests for, as one before any content is.
	// Let through at its first content, Antigravity's "the reply ended
	// before it was complete" and its 429 halfway went to the summary
	// as the only answer, the next account never asked
	wantWhole bool

	// upstreamKeys are the headers of the vendor's reply heardUpstream put
	// in header (#4145)
	upstreamKeys []string

	// over, when another candidate stands behind the try, reads what
	// passes before its answer begins for a refusal to hand to the next
	// one in the same stream (refusalHandover); handedOver says it was
	over       *refusalHandover
	handedOver bool
}

// onLoop sets what is told of a loop before it ends the reply (reask),
// nil for nobody.
func (h *holdWriter) onLoop(f func(loopTrip) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reask = f
}

// errSlowStart is what a try's writes get once it was let go for taking
// longer than its group waits for a first token.
var errSlowStart = errors.New("let go: no first token in time")

// errLooped is what a try's writes get once its reply was ended for
// looping (cutLoop).
var errLooped = errors.New("ended: the reply was stuck in a loop")

func newHoldWriter(w http.ResponseWriter, hold bool) *holdWriter {
	return &holdWriter{w: w, hold: hold, header: http.Header{}, first: firstToken{start: time.Now()}}
}

func (h *holdWriter) Header() http.Header { return h.header }

// note sets a header magpie says of the try, which keepAlive may send
// from watch's goroutine before the try has its status.
func (h *holdWriter) note(k, v string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.header.Set(k, v)
	if h.notes == nil {
		h.notes = http.Header{}
	}
	h.notes.Set(k, v)
}

func (h *holdWriter) WriteHeader(code int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.writeHeader(code)
}

func (h *holdWriter) writeHeader(code int) {
	if h.status != 0 || h.slow {
		return
	}
	h.status = code
	if h.hold && code >= 400 {
		return
	}
	if h.hold && strings.HasPrefix(h.header.Get("Content-Type"), "text/event-stream") {
		h.stream, h.since = true, time.Now()
		return
	}
	if h.hold && strings.HasPrefix(h.header.Get("Content-Type"), "application/json") {
		// read whole for a refusal once the try is over (settle)
		h.whole = true
		return
	}
	if h.hold && h.alive != nil && h.alive.sent {
		// any other reply, once the agent has a stream's headers: held
		// whole for release to send as that stream, unless it begins as
		// one
		h.whole, h.wholeUnsure = true, true
		return
	}
	h.pass()
}

func (h *holdWriter) pass() {
	if h.alive != nil && h.alive.sent {
		// the agent has the stream's headers already, from keepAlive
		h.passing = true
		return
	}
	dst := h.w.Header()
	for k, v := range h.header {
		if k != resetsHeader && k != refusedHeader && k != zdrHeader && k != answeredThereHeader && k != keyLimitThereHeader && k != namedStatusHeader { // magpie's own notes, for restAfter and settle
			dst[k] = v
		}
	}
	h.w.WriteHeader(h.status)
	h.passing, h.wrote, h.atLine = true, time.Now(), true
}

func (h *holdWriter) Write(b []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.slow {
		return 0, errSlowStart
	}
	if h.looped != "" || h.relooped {
		return 0, errLooped
	}
	if h.handedOver {
		return 0, errHandedOver
	}
	if h.status == 0 {
		h.writeHeader(http.StatusOK)
	}
	if h.loop != nil && !h.whole && h.status < 400 {
		if t, ok := h.loop.feed(b); ok {
			switch {
			case h.reask != nil && h.reask(t):
				h.reasked++
				h.loop = &loopGuard{}
			case h.reask == nil && h.mayReloop(t):
				return h.letGoLoop(b)
			default:
				return h.cutLoop(b, t)
			}
		}
	}
	h.see(b)
	h.first.see(b)
	h.heard = time.Now()
	if h.wholeUnsure {
		head := append(h.held.Bytes()[:h.held.Len():h.held.Len()], b...)
		if sse, sure := sseStart(head); sure {
			h.wholeUnsure = false
			if sse {
				// a stream under a type that doesn't say so: held as a
				// stream from here, its events read as they come, so an
				// error before anything is said still goes to the next
				// candidate. It went through at once before, and a 200
				// that only framed a reply and then failed reached the
				// agent (#1418)
				h.whole, h.stream, h.since = false, true, time.Now()
			}
		}
	}
	if h.overActive() {
		// nothing said yet: a refusal is handed to the next candidate
		h.passOver(b)
		if h.handedOver {
			return 0, errHandedOver
		}
		return len(b), nil
	}
	if h.first.first != 0 || h.passing {
		h.refusalAfter(b)
	}
	if h.passing {
		h.sent(b)
		return h.w.Write(b)
	}
	n, err := h.held.Write(b)
	if h.stream && h.failure == 0 {
		h.scan()
	}
	if h.handedOver {
		return 0, errHandedOver
	}
	return n, err
}

// cutLoop ends a reply found stuck in a loop (loopGuard): what came of it
// goes to the agent as it would have, the held part of it too, and then
// the stream's error in the agent's protocol, which Codex asks again on
// and other agents show; the vendor's request is let go, and the try's
// writes after it fail. The stream is no longer held for another to
// answer: its loop was said as it came, unless the model's reasoning is
// held for a refusal, and then it goes now.
func (h *holdWriter) cutLoop(b []byte, t loopTrip) (int, error) {
	msg := t.message()
	if h.passing {
		h.sent(b)
		h.w.Write(b)
	} else {
		h.held.Write(b)
		h.flow()
	}
	proto := provider.Chat
	if h.alive != nil {
		proto = h.alive.proto
	}
	// what is appended begins after a blank line, as in passthrough: the
	// reply's last write may have stopped mid-line, and an event joined
	// onto it is one the client can't parse (TestLoopCutMidLine)
	if h.atLine {
		io.WriteString(h.w, "\n")
	} else {
		io.WriteString(h.w, "\n\n")
	}
	// no model: the reply went ahead under its own id and model, and an
	// error that starts a message of its own after it is #3792
	streamError(h.w, proto, "", http.StatusBadGateway, msg)
	h.looped, h.ended = msg, true
	h.flush()
	if h.stop != nil {
		h.stop()
	}
	return 0, errLooped
}

// mayReloop says whether a reply passed through, found stuck in a loop
// (t), is let go for the request to ask it again (letGoLoop) rather than
// ended: its reasoning looped before it said a text or a call, the
// request has an ask left (reloop), and the next try's stream can follow
// what the agent has. Nothing of it sent, any can; sent, only a Chat
// stream, whose chunks are deltas the agent adds up: a second stream's
// follow the first's as they would its own. An Anthropic or Responses
// stream begun can't be followed by another's message_start or
// response.created, so it ends with the loop's error, as before.
func (h *holdWriter) mayReloop(t loopTrip) bool {
	if !h.reloop || !t.reasoning || h.loop.spoke || h.alive == nil || h.ctx != nil && h.ctx.Err() != nil {
		return false
	}
	return !h.passing || h.alive.proto == provider.Chat
}

// letGoLoop lets a reply go whose reasoning looped before it said a word
// (mayReloop), for the request to ask it again: the vendor's request is
// let go, and the try's writes after it fail. Held, none of it reaches
// the agent; passing, its events go up to the end of the one the loop was
// found in, for the next try's stream to begin on a whole event.
func (h *holdWriter) letGoLoop(b []byte) (int, error) {
	if h.passing {
		b = b[:max(len(b)-len(h.loop.pending), 0)]
		h.sent(b)
		h.w.Write(b)
		h.flush()
		// the next try's stream follows this one: its 200 and headers
		// are not sent again
		h.alive.sent = true
	} else {
		h.held.Reset()
	}
	h.relooped = true
	if h.stop != nil {
		h.stop()
	}
	return 0, errLooped
}

// sseStart reports whether a body that begins with head is server-sent
// events, by the field its first line begins with, and whether head is
// enough to tell.
func sseStart(head []byte) (sse, sure bool) {
	head = bytes.TrimLeft(head, " \t\r\n")
	if len(head) == 0 {
		return false, false
	}
	for _, field := range []string{"data:", "event:", "id:", "retry:", ":"} {
		if bytes.HasPrefix(head, []byte(field)) {
			return true, true
		}
		if len(head) < len(field) && strings.HasPrefix(field, string(head)) {
			return false, false
		}
	}
	return false, true
}

// streamEnds are how each protocol's stream says it is over, in every
// spelling a relay sends: SSE makes the space after "data:" and "event:"
// optional, and JSON the one after a colon (data:[DONE] from a relay was
// logged 499 when the agent hung up on it, #1978). A JSON marker can only
// appear outside a quoted string, as text that says so is escaped; an SSE
// one starts a line, which text inside the JSON can't.
var streamEnds = [][]byte{
	[]byte(`"type":"response.completed"`), []byte(`"type": "response.completed"`), // Responses
	[]byte(`"type":"response.incomplete"`), []byte(`"type": "response.incomplete"`),
	[]byte(`"type":"message_stop"`), []byte(`"type": "message_stop"`), // Anthropic
	[]byte("\nevent: message_stop"), []byte("\nevent:message_stop"),
	[]byte("\ndata: [DONE]"), []byte("\ndata:[DONE]"), // Chat Completions
}

// see notes a stream's last event going by. An agent may hang up as soon as
// it has that — Codex does — and a reply it had whole is not canceled.
func (h *holdWriter) see(b []byte) {
	if h.ended || h.ctx != nil && h.ctx.Err() != nil {
		return
	}
	buf := append(h.tail, b...)
	if len(h.tail) == 0 {
		buf = append([]byte{'\n'}, b...) // the stream starts a line
	}
	for _, m := range streamEnds {
		if bytes.Contains(buf, m) {
			h.ended = true
			return
		}
	}
	h.tail = append(h.tail[:0], buf[max(len(buf)-32, 0):]...)
}

// holdLongest is the longest a stream is held waiting for its first
// content, and holdMost the most of it.
const (
	holdLongest = 15 * time.Second
	holdMost    = 1 << 20
)

// holdBuffered is how long a stream is held once the ChatGPT backend has
// said it holds the reply back for extra safety checks (response.metadata,
// safety_buffering): gpt-6.x at xhigh said nothing for 35s and then failed
// with bio_policy, which went to Codex as its "This content can't be
// shown" once 15s had let the stream through (#248). Codex waits 300s for
// a stream's next event.
const holdBuffered = 4 * time.Minute

// holdThinking is how long a stream is held while all it has is reasoning,
// shown to nobody yet: Claude refused Claude Code after 10–25s of thinking,
// which had let the stream through, so Claude Code got the refusal ("…'s
// safeguards stopped the response above") and the next account was never
// asked (#248). A reply that goes on to say something is let through, its
// reasoning with it, as soon as it does.
const holdThinking = 4 * time.Minute

// holdLead is how long a stream with nothing but the frames that come
// before a reply is held, when its agent is kept alive meanwhile
// (keptQuiet): as long as one that only reasons, under the 300s Codex
// waits for its next event. With nothing to keep the agent alive (a
// Gemini stream), it is let through at holdLongest as before.
const holdLead = holdThinking

// keptQuiet says whether the agent of a held stream is kept alive with
// SSE comments while it is held (keepQuiet, keepAlive): it asked for a
// stream, in a protocol that takes a comment.
func (h *holdWriter) keptQuiet() bool {
	return h.streams && h.alive != nil && h.alive.proto != provider.Gemini
}

// refusesAfterThinking tells whether a model's vendor may end a reply that
// has only reasoned with its safety filter's refusal: Claude's stop_reason
// refusal, OpenAI's content_filter or bio_policy, Gemini's SAFETY (#248),
// and DeepSeek's content_filter: deepseek-v4.1-flash on WorkBuddy reasoned
// for 5s and then ended content_filter with nothing said (the plugin takes
// out the filter's canned line), and with its reasoning let through the
// group's next member was never asked and DeepSeek Harness failed "The
// model refused to complete the request" (itspig, plugins #540).
// Anybody else's reasoning — GLM's, Kimi's… — is shown as it comes: held,
// GLM on a ZCode account through a group showed Claude Code its thinking
// only once the text began, all at once (悠悠哥 on Discord).
func refusesAfterThinking(model string) bool {
	if modelFamily(model) != "" {
		return true
	}
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.HasPrefix(m, "gemini") || deepseekAfterThinking(m)
}

// deepseekAfterThinking tells whether the model is DeepSeek's, the one
// vendor whose reasoning the refusal hold lets go once the member has
// gone on from its thinking to an answer enough times running
// (holdsThinking) whether or not a refusal after it can be handed over:
// Claude's, GPT's and Gemini's only where it can (#248, #4166).
func deepseekAfterThinking(model string) bool {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.HasPrefix(m, "deepseek")
}

// thoughtCleanMost is how many thinking replies in a row that went on to
// an answer trust a member with its reasoning as it comes
// (holdsThinking): DeepSeek's filter refuses after thinking hardly ever (#4512:
// one content_filter in 33,245 group requests over 7 days), and held,
// every one of those requests kept the agent waiting its whole thinking
// out. A refusal of its own takes the trust back (thoughtRefused).
const thoughtCleanMost = 3

// holdsThinking tells whether the try's reasoning is held back for a
// refusal after it while another candidate could answer. DeepSeek's is,
// until the member's thinking has gone on to an answer thoughtCleanMost
// times running; past that it goes through as it comes, GLM's way.
// Claude's, GPT's and Gemini's is trusted the same way only where a
// refusal after it is handed to the next candidate in the same stream
// (handsOver, refusalHandover), and is held whatever the member answered
// lately where it isn't (#248): told to the agent, such a refusal would
// end the turn with nothing said (#4166).
func (s *Server) holdsThinking(c candidate, handsOver bool) bool {
	if !refusesAfterThinking(c.model) {
		return false
	}
	if !deepseekAfterThinking(c.model) && !handsOver {
		return true
	}
	s.thoughtMu.Lock()
	defer s.thoughtMu.Unlock()
	return s.thoughtClean[c.p.ID+"/"+c.model] < thoughtCleanMost
}

// thoughtAnswered counts a thinking reply that went on to an answer, for
// holdsThinking's trust to run out on.
func (s *Server) thoughtAnswered(c candidate) {
	if !refusesAfterThinking(c.model) {
		return
	}
	s.thoughtMu.Lock()
	defer s.thoughtMu.Unlock()
	if s.thoughtClean == nil {
		s.thoughtClean = make(map[string]int)
	}
	s.thoughtClean[c.p.ID+"/"+c.model]++
}

// thoughtRefused takes the trust back: the member's filter refused a
// reply after (or beside) its thinking (plugins #540), so its reasoning
// is held again for the next requests, until it has answered
// thoughtCleanMost times running.
func (s *Server) thoughtRefused(c candidate) {
	if !refusesAfterThinking(c.model) {
		return
	}
	s.thoughtMu.Lock()
	defer s.thoughtMu.Unlock()
	if s.thoughtClean == nil {
		s.thoughtClean = make(map[string]int)
	}
	s.thoughtClean[c.p.ID+"/"+c.model] = 0
}

// scan reads the held stream's events so far: an error before any content
// fails it; content, or waiting too long for it, lets it through.
func (h *holdWriter) scan() {
	for {
		rest := h.held.Bytes()[h.scanned:]
		end := eventEnd(rest)
		if end < 0 {
			break
		}
		h.scanned += end
		switch kind, status, msg := streamEvent(rest[:end]); kind {
		case eventLead:
			continue
		case eventBuffering:
			h.buffered = true
			continue
		case eventThinking:
			if h.thinkingShown && !h.wantWhole {
				h.thoughtThrough = h.hold
				h.flow()
				return
			}
			h.thinking = true
			continue
		case eventError, eventRefusalError:
			h.failure, h.failMsg = status, msg
			h.refused = kind == eventRefusalError
			if h.stop != nil {
				h.stop()
			}
			return
		case eventRefusal:
			// A refusal's stop marker can still have usage and the
			// protocol's terminator after it; an error above cannot.
			h.failure, h.failMsg, h.refused = status, msg, true
			return
		}
		if h.wantWhole {
			if why, ok := endedIncomplete(rest[:end]); ok {
				h.failure, h.failMsg = http.StatusBadGateway, why
				return
			}
			continue // content, held with the rest (whole)
		}
		h.flow()
		return
	}
	if h.wantWhole {
		return
	}
	if h.alive.tooQuiet(0) {
		// Codex has gone as long as it waits for an event: what is held
		// goes now, or it hangs up (agentQuietMost)
		h.flow()
		return
	}
	longest := holdLongest
	if h.buffered {
		longest = holdBuffered
	}
	if h.thinking {
		longest = max(longest, holdThinking)
	}
	if h.keptQuiet() {
		// a stream of nothing but its frames — response.created, pings,
		// an empty message begun — while its agent is kept alive with
		// comments: let through at 15s, the 200 the agent then had made
		// the vendor's server_is_overloaded 21s later the agent's error,
		// with the group's next member never asked (#1418)
		longest = max(longest, holdLead)
	}
	// a group that asks the next member when one is slow to start holds
	// the stream until then, for nothing of it to have reached the agent
	waiting := h.firstWait > 0 && h.first.first == 0
	if waiting {
		longest = max(longest, h.firstWait+time.Second)
	}
	if h.held.Len() > holdMost || time.Since(h.since) > longest {
		h.flow()
		return
	}
	if (h.thinking || h.buffered || waiting || h.keptQuiet()) && (time.Since(h.since) >= keepHeldAfter || h.alive != nil && h.alive.sent) {
		h.keepAlive()
	}
}

// keepHeldAfter is how long a held stream waits before its agent is first
// kept alive (keepAlive): the stream's 200, then a comment every
// keepHeldEvery. A refusal after it still goes to another account, whose
// stream follows the comments in the same response. It was holdLongest,
// 15s, and a proxy between the agent and magpie that hangs up on a
// request with nothing back for about 10s ended every turn whose vendor
// took longer to its first event: Codex on Windows sends its requests for
// 127.0.0.1 through the system proxy a Clash-style app sets, "127.*" in
// its bypass list notwithstanding (codexProxyFor), and they ended 499
// (mrten_95615 on Discord). A failure before it is still a status, its
// Retry-After with it.
var keepHeldAfter = 3 * time.Second

// keepHeldEvery is the longest a held stream's agent goes without a
// comment once it has its 200, under keepaliveEvery: the same proxy waits
// for bytes, not for events.
var keepHeldEvery = 5 * time.Second

// keptAlive is what a request's held streams have sent its agent ahead of
// a reply: the stream's headers and SSE comments, nothing of the reply.
type keptAlive struct {
	proto provider.Protocol
	// model is the one the agent asked for, which an error sent in the
	// stream names (streamError, #3728)
	model string
	sent  bool      // the stream's 200 and headers went out
	at    time.Time // the last comment
	// told is when the agent was last sent anything of a reply, else when
	// it asked; streams, whether it asked for a stream
	told    time.Time
	streams bool
	// codex, whether Codex sent the request (fromCodex)
	codex bool
	// whole, a reply wanted only whole (wantsWhole): a compaction's summary
	// ask, written into codexCompact's recorder, which Codex never reads.
	// It is never sent a 200 or a comment: once one went, a refusal after
	// it came as the stream's error, not its status, and a summary over
	// the vendor's byte cap whose fitting tries took past keepHeldAfter (a
	// 40 MB upload asked four times) failed 502 instead of being made
	// without a model (#4862)
	whole bool
	// Only opted-in Claude cache reporting can defer initial keepalives.
	// Selection releases it before process startup; expiry keeps the stream alive.
	cacheReportUntil time.Time
}

// agentQuietMost is the longest a streaming Responses agent goes with no
// event while its tries are held, one after another, and the pauses
// between them. Codex counts only events toward its stream idle timeout,
// 300s by default, not the SSE comments keepAlive sends: codex-cli 0.162
// on a server sending nothing but ": keepalive" every 2s, with
// stream_idle_timeout_ms 6000, failed "stream disconnected before
// completion: idle timeout waiting for SSE" and reconnected (fadenoob on
// Discord). Each try was held for up to holdThinking or holdLead by
// itself, so a second held try, or a group's pause (#1418), went past it.
var agentQuietMost = 4 * time.Minute

// compactsInStream says a refusal of a request too large to be taken —
// past the vendor's byte cap (413) or the model's window (tooLong) — goes
// to the agent in its stream though none was sent yet: Codex compacts on
// response.failed context_length_exceeded alone. Told by status, it takes
// a 413 for an unexpected status and a 400 context_length_exceeded for an
// invalid request, and either ends the turn with nothing compacted, a
// compaction refused the same way failing outright (codex-rs codex-api
// api_bridge.rs). Another client keeps the status, and so does magpie's
// own summary ask (whole), whose 413 codexCompact reads as its cue to ask
// the conversation as text and then compact without a model.
func (a *keptAlive) compactsInStream(status int, msg string) bool {
	return a.codex && !a.whole && a.streams && a.proto == provider.Responses && (status == http.StatusRequestEntityTooLarge || tooLong(status, msg))
}

// tooQuiet says whether a Responses agent that has had no event of its
// reply would go agentQuietMost without one after d more.
func (a *keptAlive) tooQuiet(d time.Duration) bool {
	return a != nil && a.streams && a.proto == provider.Responses && !a.told.IsZero() && time.Since(a.told)+d >= agentQuietMost
}

// keepAlive tells the agent of a stream held past keepHeldAfter for its
// reasoning that it goes on, every keepHeldEvery: held for up to
// holdThinking (4 minutes), the agent heard nothing at all, and Alma's
// idle timeout for a reasoning model, 240s, ended the turn first (#751). A
// comment is no event in any protocol, so another account may still answer
// after it, its stream going on in the same response (pass).
func (h *holdWriter) keepAlive() {
	a := h.alive
	// a Gemini stream has none, as geminiEncoder.keepalive: agy fails on
	// the comment ("invalid stream chunk: : keepalive", #934)
	if a == nil || a.whole || a.proto == provider.Gemini || time.Since(a.at) < min(keepaliveEvery, keepHeldEvery) {
		return
	}
	if !a.sent {
		if !a.cacheReportUntil.IsZero() {
			if time.Now().Before(a.cacheReportUntil) {
				return
			}
			a.cacheReportUntil = time.Time{}
			h.header.Del("X-Magpie-Cache-Marks-Dropped")
			h.notes.Del("X-Magpie-Cache-Marks-Dropped")
			log.Printf("claude: cache-mark report timed out before run selection; streaming without the dropped-mark header")
		}
		// the try's header is whole once it has its status (WriteHeader,
		// under mu); before, the vendor's are still being copied into it
		// from the try's goroutine, and only magpie's notes are read
		src := h.header
		if h.status == 0 {
			src = h.notes
		}
		dst := h.w.Header()
		for k, v := range src {
			if k != resetsHeader && k != refusedHeader && k != zdrHeader && k != answeredThereHeader && k != keyLimitThereHeader && k != namedStatusHeader && k != "Content-Length" {
				dst[k] = slices.Clone(v)
			}
		}
		dst.Set("Content-Type", "text/event-stream; charset=utf-8")
		h.w.WriteHeader(http.StatusOK)
		a.sent = true
	}
	// no blank line after it, as sseWriter.comment
	_, _ = io.WriteString(h.w, ": keepalive\n")
	a.at = time.Now()
	h.wrote, h.atLine = a.at, true
	if f, ok := h.w.(http.Flusher); ok {
		f.Flush()
	}
}

// sent notes b going to the agent.
func (h *holdWriter) sent(b []byte) {
	if len(b) > 0 {
		h.wrote, h.atLine = time.Now(), b[len(b)-1] == '\n'
		if h.alive != nil {
			h.alive.told = h.wrote
		}
	}
}

// refusalAfter reads the events of a reply that has begun, for the safety
// filter's refusal in one of them. The reply goes through as it comes, so
// the scan that reads a stream held for its first content never sees an
// event that comes after it, and the filter refusing a turn it had begun
// is no failure of the account, as one it refuses before any of it is said
// is not (#248). A refusal that is the vendor's own finish code — Chat
// Completions' content_filter, Anthropic's stop_reason refusal — on a
// client reading Chat chunks hands the try over (#5075, handOffAfter):
// the line the vendor put out first stays, as the reasoning of a
// handed-over try does. What is read is kept only as far as the event it
// is in is whole, as a write may end in the middle of one.
func (h *holdWriter) refusalAfter(b []byte) {
	// Every write is read, not only one that looks like a refusal: an
	// event's name may come in one write and its data in the next (#2400).
	// Only an unfinished event's bytes are kept.
	if !h.calls {
		h.calls = mayCall(b)
	}
	buf := b
	if len(h.after) > 0 {
		h.after = append(h.after, b...)
		buf = h.after
	}
	at := len(buf) - len(b) // where this write's bytes begin in buf
	gone := 0               // how much of buf whole events have taken
	for {
		end := eventEnd(buf[gone:])
		if end < 0 {
			break
		}
		ev := buf[gone : gone+end]
		gone += end
		if mayRefuse(ev) {
			if kind, status, msg := streamEvent(ev); kind == eventRefusal || kind == eventRefusalError {
				h.refusedAfter = true
				// an error event mid-reply is a stream's accident, kept as
				// it came (#733); the finish code is the vendor's word that
				// the turn it had begun was never answered (#5075)
				if kind == eventRefusal && h.handsOff() {
					// what of this write went by before the code — the
					// reply's last words — goes out with the rest; the
					// code, and what came with it, does not
					if start := gone - len(ev); start > at {
						pre := buf[at:start]
						h.sent(pre)
						h.w.Write(pre)
					}
					h.handOffAfter(status, msg)
					return
				}
			}
		}
	}
	buf = buf[gone:]
	if len(buf) == 0 || len(buf) > holdMost {
		h.after = nil
		return
	}
	h.after = append([]byte(nil), buf...)
}

// mayCall says whether a write of the reply carries a tool call of some
// kind: Chat's delta.tool_calls or its legacy function_call, Anthropic's
// tool_use, Responses' function_call. A word of it in plain text counts
// too — the cost of missing one, a call the agent would run beside the
// next member's, is the greater.
func mayCall(b []byte) bool {
	return bytes.Contains(b, []byte(`"tool_calls"`)) ||
		bytes.Contains(b, []byte(`"tool_use"`)) ||
		bytes.Contains(b, []byte(`"function_call"`))
}

// mayRefuse is whether an event may be the safety filter's refusal, by the
// words its shapes carry, before it is read as JSON at all: every event of
// a reply that has begun goes through this.
func mayRefuse(ev []byte) bool {
	for _, w := range refusalSays {
		if bytes.Contains(ev, w) {
			return true
		}
	}
	return false
}

// refusalSays are the words the events a vendor's filter refuses a reply
// with carry: an error's code, or its event's name (event: error, with
// only a message for data), a stop reason's, a finish reason's, a Gemini
// block reason's.
var refusalSays = [][]byte{
	[]byte(`error`), []byte(`failed`), []byte(`refusal`), []byte(`filter`),
	[]byte(`incomplete`), []byte(`finishReason`), []byte(`blockReason`),
	[]byte(`"sensitive"`), // Zhipu's finish_reason
}

// flow lets a held stream through, and what follows it.
func (h *holdWriter) flow() {
	if h.handedOver {
		return
	}
	h.pass()
	if h.overActive() {
		held := bytes.Clone(h.held.Bytes())
		h.held.Reset()
		h.passOver(held)
		return
	}
	h.sent(h.held.Bytes())
	h.w.Write(h.held.Bytes())
	h.held.Reset()
	h.flush()
}

func (h *holdWriter) Flush() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.flush()
}

func (h *holdWriter) flush() {
	if f, ok := h.w.(http.Flusher); ok && h.passing {
		f.Flush()
	}
}

// code is the status a try failed with: the reply's, or its stream's
// error's.
func (h *holdWriter) code() int {
	if h.failure != 0 {
		return h.failure
	}
	return h.status
}

// errBody is what the vendor said of the failure.
func (h *holdWriter) errBody() []byte {
	if h.failure != 0 {
		return []byte(h.failMsg)
	}
	return h.held.Bytes()
}

// settle reads a reply held, once the try is over, for a refusal with
// nothing said: a reply held whole, or an error status that is one.
func (h *holdWriter) settle() {
	if h.passing || h.failure != 0 {
		return
	}
	if h.status >= 400 {
		// an error status saying the safety filter refused it: OpenAI's
		// 400 bio_policy, which Codex was handed with the next account
		// never asked (#248)
		if msg := h.header.Get(refusedHeader); msg != "" {
			h.failure, h.failMsg, h.refused = refusedStatus, msg, true
		} else if msg, ok := policyRefusal(h.held.Bytes()); ok {
			h.failure, h.failMsg, h.refused = refusedStatus, msg, true
		}
		return
	}
	if h.wantWhole && h.stream && h.status < 400 && !h.ended && (h.ctx == nil || h.ctx.Err() == nil) {
		// a reply wanted whole that stopped before its last event, with
		// no error said (#2274)
		h.failure, h.failMsg = http.StatusBadGateway, errEndedShort.Error()
		return
	}
	if !h.whole {
		return
	}
	if msg, ok := refusedReply(h.held.Bytes()); ok {
		h.failure, h.failMsg, h.refused = refusedStatus, msg, true
		return
	}
	if h.status < 400 {
		// a 200 that is only an error fails as that error's own status
		// would (errorReply): asked again, or of the next, not handed on
		// as an answer
		if status, msg, ok := errorReply(h.held.Bytes()); ok {
			h.failure, h.failMsg = status, msg
			return
		}
	}
	if h.alive != nil && h.alive.sent && h.status < 400 && wholeEvents(h.alive.proto, h.held.Bytes()) == nil {
		// a 200 that is no reply of the protocol's, once the agent has a
		// stream's headers: it can't go as that stream, so it failed, as
		// a translated reply that didn't stream does, for the next to
		// answer
		h.failure, h.failMsg = http.StatusBadGateway, didNotStream+": "+provider.APIError(h.held.Bytes(), "unexpected reply")
	}
}

// failed reports a held error another provider could answer instead.
func (h *holdWriter) failed() bool {
	if h.refused {
		return !h.passing
	}
	return !h.passing && h.code() >= 400 && retryable(h.code(), h.errBody())
}

// mayAskAgain tells whether a failure now is still held, for the request
// to ask again or of another: nothing of the try has reached the agent.
func (h *holdWriter) mayAskAgain() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hold && !h.passing
}

// thoughtOnly tells whether the try went through on its reasoning while
// it was still held for the request to ask again (thoughtThrough).
func (h *holdWriter) thoughtOnly() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.thoughtThrough
}

// sawThinking tells whether the try reasoned at all, held or as it came.
func (h *holdWriter) sawThinking() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.thinking || h.thoughtThrough
}

// handBack sends a refusal held with nothing said as the vendor's own
// reply, a 200 that ends with its stop reason, and says whether it could:
// not a refusal said as an error status or event, which is no reply.
func (h *holdWriter) handBack() bool {
	if !h.refused || h.passing || h.status == 0 || h.status >= 400 || h.held.Len() == 0 {
		return false
	}
	h.failure, h.failMsg = 0, ""
	h.release()
	return true
}

// refusalAsksAgain says whether a request refused n times by the vendor's
// safety filter is asked of the next account or member left: as many
// times as Settings' RefusalRetries says, every one left when it is unset
// (#248).
func refusalAsksAgain(n int) bool {
	retries := settings.Load().RefusalRetries
	return retries == nil || *retries < 0 || n <= *retries
}

// release sends a held reply after all: nobody else is left to try.
func (h *holdWriter) release() {
	if h.passing || h.status == 0 {
		return
	}
	if h.alive != nil && h.alive.sent && !h.stream {
		h.passing = true
		switch {
		case h.failure != 0:
			// a 200 that is no reply of the protocol's (settle)
			who := cmp.Or(h.header.Get(providerHeader), "the provider")
			streamError(h.w, h.alive.proto, h.alive.model, h.failure, who+" "+h.failMsg)
		case h.status >= 400:
			// an error status, once the agent has a stream: told as its error
			streamError(h.w, h.alive.proto, h.alive.model, h.status, provider.APIError(h.held.Bytes(), http.StatusText(h.status)))
		default:
			// a reply given whole by a vendor that ignored stream:true,
			// after the agent was kept alive: sent as the stream it asked
			// for, not as an error "OK" with the reply in it
			wholeAsStream(h.w, h.alive.proto, h.held.Bytes())
		}
		return
	}
	h.pass()
	h.w.Write(h.held.Bytes())
	h.flush()
}

// watchEvery is how often watch looks at a try.
var watchEvery = time.Second

// keepQueued keeps the agent of a stream alive while its try waits for a
// slot of its key's or account's (MaxConcurrency) or for room in its
// minute (MaxRPM), before anything is sent to the vendor: past
// keepHeldAfter it is sent the stream's 200 and SSE comments
// (keepAlive), every keepHeldEvery for as long as the wait lasts, as a
// try's held stream is. A wait of up to 2 minutes for the minute, or
// QueueWait's for a slot, sent the agent nothing at all, which an agent's
// or a proxy's timeout for its headers ended first (coeo91 on Discord:
// WorkBuddy, Trae and Qoder said the request timed out). Once they are
// sent the try is held (hold), so that a failure, or the queue turning it
// away, reaches the agent as the stream's error (failTo), and another
// candidate may still answer in the same stream. A wait shorter than
// keepHeldAfter sends nothing, its 429 still a status with its
// Retry-After. The returned func ends it, and returns once it has.
func (h *holdWriter) keepQueued() func() {
	if !h.streams || h.alive == nil || h.alive.proto == provider.Gemini {
		return func() {}
	}
	done, over := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(over)
		tick := time.NewTicker(watchEvery)
		defer tick.Stop()
		began := time.Now()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				h.mu.Lock()
				if (time.Since(began) >= keepHeldAfter || h.alive.sent) && (h.ctx == nil || h.ctx.Err() == nil) {
					h.keepAlive()
					if h.alive.sent {
						h.hold = true
					}
				}
				h.mu.Unlock()
			}
		}
	}()
	return func() {
		close(done)
		<-over
	}
}

// watch lets the try go — stop, with slow said — once firstWait passes
// with no first content and nothing of it sent to the agent: the stream
// held, or no reply begun at all. And it keeps the agent of a stream alive
// while the vendor says nothing (keepQuiet), a vendor that says nothing
// running no scan. The returned func ends the watch, and returns once it
// has.
func (h *holdWriter) watch() func() {
	first := h.firstWait > 0 && h.stop != nil
	if !first && !h.streams {
		return func() {}
	}
	done, over := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(over)
		var deadline <-chan time.Time
		if first {
			t := time.NewTimer(h.firstWait)
			defer t.Stop()
			deadline = t.C
		}
		tick := time.NewTicker(watchEvery)
		defer tick.Stop()
		h.mu.Lock()
		began := time.Now()
		if h.heard.IsZero() {
			h.heard = began // the request to the vendor, nothing heard yet
		}
		h.mu.Unlock()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				h.mu.Lock()
				if first && h.started() {
					first = false
				}
				if !first && !h.streams {
					h.mu.Unlock()
					return
				}
				// an agent already kept alive (keepQueued, an earlier try)
				// goes on hearing one every keepaliveEvery: waiting
				// keepHeldAfter again left it up to twice that with none
				if time.Since(began) >= keepHeldAfter || h.alive != nil && h.alive.sent {
					h.keepQuiet()
				}
				h.mu.Unlock()
			case <-deadline:
				deadline = nil
				if !first {
					continue
				}
				h.mu.Lock()
				if !h.started() {
					h.slow = true
				}
				h.mu.Unlock()
				if h.slow {
					h.stop()
					return
				}
				first = false
			}
		}
	}()
	return func() {
		close(done)
		<-over
	}
}

// keepQuiet keeps the agent of a stream alive with SSE comments while the
// vendor says nothing, every keepaliveEvery for up to keepaliveLongest
// since it last did: a vendor that sent its 200 and response.created, then
// nothing while it reasoned, had the stream held with none of it sent,
// and Cloudflare in front of a remote magpie ended the request at 125s
// (524) with the agent sent nothing at all; one quiet mid-reply was reset
// by it as well (#947). Held, the agent is sent the stream's headers and
// comments only (keepAlive), none of the try, which another account may
// still answer instead; passed, a comment goes between two lines of it.
func (h *holdWriter) keepQuiet() {
	if h.slow || h.relooped || h.ended || h.alive == nil || h.alive.proto == provider.Gemini || h.ctx != nil && h.ctx.Err() != nil {
		return
	}
	if !h.heard.IsZero() && time.Since(h.heard) >= keepaliveLongest {
		return // a stream stuck this long is left for a timeout to end
	}
	if !h.passing && h.stream && h.failure == 0 && !h.whole && !h.wantWhole && h.held.Len() > 0 && h.alive.tooQuiet(0) {
		// a stream held while its vendor is quiet: Codex would hang up
		// before the next event let it through (agentQuietMost)
		h.flow()
		return
	}
	if !h.passing {
		// only a stream, no reply yet to an agent that asked for one, or
		// one yet to say whether it streams (wholeUnsure), none of it sent;
		// an error held is told as the stream's own once the 200 is out
		if h.hold && h.failure == 0 && (!h.whole || h.wholeUnsure) && h.status < 400 && (h.stream || h.status == 0 || h.wholeUnsure) {
			h.keepAlive()
		}
		return
	}
	if !h.atLine || time.Since(h.wrote) < keepaliveEvery || !strings.HasPrefix(h.w.Header().Get("Content-Type"), "text/event-stream") {
		return
	}
	// no blank line after it, as sseWriter.comment
	_, _ = io.WriteString(h.w, ": keepalive\n")
	h.wrote = time.Now()
	if f, ok := h.w.(http.Flusher); ok {
		f.Flush()
	}
}

// started says whether the try has begun its reply, or ended, as far as
// watch need know: its first content, anything sent on to the agent,
// a failure, or a reply that isn't streamed.
func (h *holdWriter) started() bool {
	return h.first.first != 0 || h.passing || h.ended || h.failure != 0 || h.whole || h.status >= 400
}

// endedIncomplete says a Responses event ends its reply cut short —
// response.incomplete, or a completed one whose status says so — and why:
// for a reply wanted whole (wantWhole), no answer (#2274). A filter's
// reason is a refusal, read before this (streamEvent).
func endedIncomplete(ev []byte) (string, bool) {
	if !bytes.Contains(ev, []byte("incomplete")) {
		return "", false
	}
	var data []byte
	for _, ln := range bytes.Split(ev, []byte("\n")) {
		if ln = bytes.TrimRight(ln, "\r"); bytes.HasPrefix(ln, []byte("data:")) {
			data = append(data, bytes.TrimSpace(ln[5:])...)
		}
	}
	var v struct {
		Type     string `json:"type"`
		Response struct {
			Status            string `json:"status"`
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &v) != nil {
		return "", false
	}
	if v.Type != "response.incomplete" && (v.Type != "response.completed" || v.Response.Status != "incomplete") {
		return "", false
	}
	why := "unknown"
	if d := v.Response.IncompleteDetails; d != nil && d.Reason != "" {
		why = d.Reason
	}
	return incompleteReply + " (" + why + ")", true
}

// incompleteReply opens what a reply wanted whole that ended incomplete
// failed with (endedIncomplete).
const incompleteReply = "the reply ended incomplete"

// eventEnd is where the first whole event in b ends, or -1.
func eventEnd(b []byte) int {
	for i := 0; i+1 < len(b); i++ {
		if b[i] != '\n' {
			continue
		}
		if b[i+1] == '\n' {
			return i + 2
		}
		if b[i+1] == '\r' && i+2 < len(b) && b[i+2] == '\n' {
			return i + 3
		}
	}
	return -1
}

const (
	eventContent = iota
	eventLead    // what comes before a reply's content: a start, a ping
	eventError
	eventRefusal      // the reply's end, by the vendor's safety filter
	eventRefusalError // the filter's error event: canceled like eventError
	eventBuffering    // a lead saying the reply is held back for safety checks
	eventThinking     // reasoning, before anything is said: a refusal may yet end it
)

// refusedStatus is what a refusal with nothing said is answered as: a
// request the agent is told not to send again as it is, rather than a
// reply that looks whole but empty, which Codex asks for again and again,
// each time paying for the prompt (#248).
const refusedStatus = http.StatusBadRequest

// filterReasons are how a vendor says its safety filter stopped the reply:
// Anthropic's stop_reason, OpenAI's finish_reason and incomplete reason,
// an OpenAI or Azure error's code, Factory's error code, and Gemini's
// finishReason and blockReason.
var filterReasons = map[string]string{
	"refusal":                  "refusal",
	"content_filter":           "content_filter",
	"content_policy_violation": "content_filter",
	// Factory's usage policy check, a 403 whose code droid 0.245.0 tells
	// apart from an account's 403 the same way (the Factory plugin keeps
	// the code on the error it hands magpie): handed on as its 403, the
	// agent took it for a failed sign-in (sweanng on Discord, ZCode:
	// "Provider authentication failed", reason=auth_failed status=403)
	"content_policy_rejected": "content_policy_rejected",
	// StepFun's error type, its filter refusing with an HTTP 451 (#5089):
	// read from the error object, the refusal is the request's, and the
	// group's next member is asked rather than the 451 handed on
	"censorship_blocked": "censorship_blocked",
	// Zhipu's finish_reason when its filter stops GLM's reply, and
	// DashScope's error code when Alibaba's stops Qwen's: both after the
	// reasoning went to the agent, which had it end there (yhwangl on X)
	"sensitive":              "sensitive",
	"data_inspection_failed": "data_inspection_failed",
	"DataInspectionFailed":   "data_inspection_failed",
	"SAFETY":                 "safety",
	"PROHIBITED_CONTENT":     "safety",
	"BLOCKLIST":              "safety",
	"SPII":                   "safety",
	"IMAGE_SAFETY":           "safety",
}

// finishFailed is what a 200 stream the vendor ends with finish_reason
// "error" (#2471) says of itself, in the failure the hold keeps: the turn
// failed, as the word says, and the group's next member answers it, while
// taken for content it reached the agent and the try stood as a success.
const finishFailed = `the reply ended with finish_reason "error"`

// emptyCompleted is what a 200 Responses stream the vendor ends with a
// response.completed that named its output empty and its usage zero
// (#2471, MurmuredVelpro) says of itself, in the failure the hold keeps:
// the turn was answered with nothing, as a translated reply that said
// nothing and counted nothing is (repliesCounted, #2086), and the
// group's next member answers it, while taken for a reply's end it
// reached the agent as a completed turn and the try stood as a success.
// One that counted a token stays a reply, as Claude's own empty end_turn
// does; one that names neither field is taken as it came, a relay's
// completed carrying neither — the fields said are what is read.
const emptyCompleted = `the reply completed with nothing in it and counted no tokens`

// outputEmpty tells whether a Responses reply named its output as carrying
// nothing: an empty list, or null — the field said, not left out.
func outputEmpty(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return string(raw) == "null" || string(raw) == "[]"
}

// refusedNote is what a refusal is noted as: the vendor's own reason.
func refusedNote(reason string) string {
	return "safety filter: " + reason
}

// refusedDetail is a refusal's note with what Anthropic's classifier said
// of it in stop_details, when it said anything (#1768: which of cyber,
// bio, frontier_llm… it was, for a user to tell one refusal from
// another): its category as given, unknown ones too, and its
// explanation, the vendor's own words, kept to a line. None is made up
// when it gave none.
func refusedDetail(reason string, details json.RawMessage) string {
	var d struct {
		Category    *string `json:"category"`
		Explanation *string `json:"explanation"`
	}
	note := refusedNote(reason)
	if len(details) == 0 || json.Unmarshal(details, &d) != nil {
		return note
	}
	var said []string
	if d.Category != nil && strings.TrimSpace(*d.Category) != "" {
		said = append(said, "category "+strings.TrimSpace(*d.Category))
	}
	if d.Explanation != nil && strings.TrimSpace(*d.Explanation) != "" {
		e := strings.Join(strings.Fields(*d.Explanation), " ")
		if r := []rune(e); len(r) > 200 {
			e = string(r[:200]) + "…"
		}
		said = append(said, e)
	}
	if len(said) == 0 {
		return note
	}
	return note + " (" + strings.Join(said, ": ") + ")"
}

// streamPart is what one event of a reply carries of its content, in any
// protocol, to tell a reply begun from its empty frame (#248).
type streamPart struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Think string `json:"thinking"`
	// a Responses item's
	Content []struct {
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`
}

// thought tells whether a part is reasoning, said or not yet: a thinking
// block, a reasoning item or its summary. It says nothing to the reader.
func (p streamPart) thought() bool {
	switch p.Type {
	case "thinking", "redacted_thinking", "summary_text", "reasoning_text", "reasoning":
		return true
	}
	return false
}

// said tells whether a part says anything an agent would show or act on:
// text, reasoning, a call. Of what isn't known, a call or a tool's part
// (web_search_call, mcp_tool_use, server_tool_use, ...) says something;
// anything else only frames the reply until something does (#1418).
func (p streamPart) said() bool {
	switch p.Type {
	case "text", "output_text", "summary_text", "reasoning_text":
		return p.Text != ""
	case "thinking":
		return p.Think != ""
	case "redacted_thinking":
		return false
	case "message":
		for _, c := range p.Content {
			if c.Text != "" || c.Refusal != "" {
				return true
			}
		}
		return false
	case "reasoning":
		for _, c := range p.Summary {
			if c.Text != "" {
				return true
			}
		}
		for _, c := range p.Content {
			if c.Text != "" {
				return true
			}
		}
		return false
	}
	return strings.Contains(p.Type, "call") || strings.Contains(p.Type, "tool") || strings.HasPrefix(p.Type, "mcp_")
}

// streamEvent says what one server-sent event of a reply is, in any of the
// protocols an agent speaks: the start of a reply, its content, or an
// error — with the status that error stands for.
func streamEvent(ev []byte) (kind, status int, msg string) {
	var name string
	var data []byte
	for _, ln := range bytes.Split(ev, []byte("\n")) {
		ln = bytes.TrimRight(ln, "\r")
		switch {
		case bytes.HasPrefix(ln, []byte("event:")):
			name = strings.TrimSpace(string(ln[6:]))
		case bytes.HasPrefix(ln, []byte("data:")):
			data = append(data, bytes.TrimSpace(ln[5:])...)
		}
	}
	if name == "" && len(data) == 0 {
		return eventLead, 0, "" // a comment, a keep-alive
	}
	var v struct {
		Type     string          `json:"type"`
		Error    json.RawMessage `json:"error"`
		Message  json.RawMessage `json:"message"` // a Responses error's; an Anthropic start's is the message
		Response struct {
			Error             json.RawMessage `json:"error"`
			Status            string          `json:"status"`
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
			Output json.RawMessage `json:"output"`
			Usage  *rUsage         `json:"usage"`
		} `json:"response"`
		Choices *[]struct {
			Delta        map[string]any `json:"delta"`
			FinishReason *string        `json:"finish_reason"`
		} `json:"choices"`
		// Anthropic's: a text delta's, or message_delta's stop_reason;
		// Responses': a string of text
		Delta        json.RawMessage `json:"delta"`
		ContentBlock streamPart      `json:"content_block"`
		Item         streamPart      `json:"item"`
		Part         streamPart      `json:"part"`
		Text         *string         `json:"text"`
		Candidates   *[]struct {
			Content struct {
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	if json.Unmarshal(data, &v) != nil {
		return eventContent, 0, "" // [DONE], or what isn't ours to read
	}
	typ := v.Type
	if typ == "" {
		typ = name
	}
	errOf := func(raw json.RawMessage) (int, int, string) {
		msg := string(raw)
		var m string
		if json.Unmarshal(v.Message, &m) == nil && m != "" {
			msg += " " + m
		}
		if said := namedStatusOf(raw); said != 0 {
			// the error names its own status, and it is the one it fails
			// as (#4855), not the one its words guess
			return eventError, said, msg
		}
		return eventError, streamStatus(msg), msg
	}
	refusal := func(reason string) (int, int, string) {
		return eventRefusal, refusedStatus, refusedNote(reason)
	}
	switch {
	case typ == "error" || typ == "response.failed":
		raw := v.Error
		if len(v.Response.Error) > 0 && string(v.Response.Error) != "null" {
			raw = v.Response.Error
		}
		if len(raw) == 0 || string(raw) == "null" {
			// Responses' own error event: its code and message on it
			if note, ok := policyRefusal(data); ok {
				return eventRefusalError, refusedStatus, note
			}
		}
		if note, ok := policyRefusal(raw); ok {
			return eventRefusalError, refusedStatus, note
		}
		return errOf(raw)
	case len(v.Error) > 0 && string(v.Error) != "null":
		if note, ok := policyRefusal(v.Error); ok {
			return eventRefusalError, refusedStatus, note
		}
		return errOf(v.Error)
	// reasoning, before a word of the reply: held with it, and longer, for
	// a refusal after it to go to another as one with nothing said (#248)
	case typ == "content_block_start" && v.ContentBlock.thought(),
		typ == "content_block_delta" && anthropicDeltaThinks(v.Delta),
		(typ == "response.output_item.added" || typ == "response.output_item.done") && v.Item.thought(),
		(typ == "response.reasoning_summary_part.added" || typ == "response.reasoning_summary_part.done") && v.Part.thought(),
		typ == "response.reasoning_summary_text.delta", typ == "response.reasoning_text.delta",
		typ == "response.reasoning_summary_text.done", typ == "response.reasoning_text.done":
		return eventThinking, 0, ""
	// what only frames a reply, before anything is said in it — held with
	// its start, so a refusal after it can still go to another
	case typ == "content_block_start" && !v.ContentBlock.said(),
		typ == "content_block_stop",
		typ == "content_block_delta" && !anthropicDeltaSays(v.Delta),
		typ == "response.output_item.added" && !v.Item.said(),
		typ == "response.output_item.done" && !v.Item.said(),
		typ == "response.content_part.added" && !v.Part.said(),
		typ == "response.content_part.done" && !v.Part.said(),
		typ == "response.reasoning_summary_part.added" && !v.Part.said(),
		typ == "response.reasoning_summary_part.done" && !v.Part.said(),
		(typ == "response.output_text.delta" || typ == "response.reasoning_summary_text.delta" || typ == "response.reasoning_text.delta") && string(v.Delta) == `""`,
		(typ == "response.output_text.done" || typ == "response.reasoning_summary_text.done" || typ == "response.reasoning_text.done") && v.Text != nil && *v.Text == "":
		return eventLead, 0, ""
	case typ == "message_delta":
		var d struct {
			StopReason  string          `json:"stop_reason"`
			StopDetails json.RawMessage `json:"stop_details"`
		}
		if json.Unmarshal(v.Delta, &d) == nil && d.StopReason == "refusal" {
			return eventRefusal, refusedStatus, refusedDetail(d.StopReason, d.StopDetails)
		}
	case typ == "response.incomplete" || typ == "response.completed" && v.Response.Status == "incomplete":
		if d := v.Response.IncompleteDetails; d != nil {
			if r, ok := filterReasons[d.Reason]; ok {
				return refusal(r)
			}
		}
	case typ == "response.completed" && v.Response.Status != "incomplete" &&
		outputEmpty(v.Response.Output) && v.Response.Usage != nil && uncounted(v.Response.Usage.usage()):
		// completed, and the reply named its output empty and its usage
		// zero (#2471, MurmuredVelpro): no reply at all, as a translated
		// one that said nothing and counted nothing is (repliesCounted,
		// #2086). An event this reaches is one nothing was said before —
		// the hold lets the first content through and reads no event
		// beside it — so nothing of the try has reached the agent, and
		// the failure is still held for the group's next candidate
		return eventError, streamStatus(emptyCompleted), emptyCompleted
	case typ == "" && v.Candidates != nil:
		// a Gemini chunk
		if r, ok := filterReasons[v.PromptFeedback.BlockReason]; ok {
			return refusal(r)
		}
		thinks := false
		for _, c := range *v.Candidates {
			for _, p := range c.Content.Parts {
				var part struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				}
				if json.Unmarshal(p, &part) == nil && part.Thought && part.Text != "" {
					thinks = true // its reasoning, said to nobody yet
					continue
				}
				if json.Unmarshal(p, &part) != nil || part.Text != "" || !bytes.Contains(p, []byte(`"text"`)) {
					return eventContent, 0, "" // text, or a call
				}
			}
			if r, ok := filterReasons[c.FinishReason]; ok {
				return refusal(r)
			}
			if c.FinishReason != "" {
				return eventContent, 0, ""
			}
		}
		if thinks {
			return eventThinking, 0, ""
		}
		return eventLead, 0, ""
	case typ == "" && len(v.PromptFeedback.BlockReason) > 0:
		if r, ok := filterReasons[v.PromptFeedback.BlockReason]; ok {
			return refusal(r)
		}
	case typ == "ping", typ == "message_start", typ == "response.created", typ == "response.in_progress", typ == "response.queued":
		return eventLead, 0, ""
	case typ == "response.metadata":
		// the ChatGPT backend's word on the turn, ahead of the reply: its
		// safety buffering, moderation, a verification it recommends.
		// Taken for content, it let the stream through, and the
		// response.failed bio_policy after it went to Codex (#248)
		if bytes.Contains(data, []byte(`"safety_buffering"`)) {
			return eventBuffering, 0, ""
		}
		return eventLead, 0, ""
	case strings.HasPrefix(typ, "codex."):
		// the ChatGPT backend's word on the account (codex.rate_limits),
		// ahead of the reply: taken for content, it let the stream
		// through, and a refusal after it (response.failed) went to the
		// agent rather than tried again
		return eventLead, 0, ""
	case typ == "" && v.Choices != nil:
		// a Chat chunk: the first says only who speaks
		thinks := false
		for _, c := range *v.Choices {
			for k, x := range c.Delta {
				if k == "role" || x == nil || x == "" {
					continue
				}
				if k != "reasoning_content" && k != "reasoning" {
					return eventContent, 0, ""
				}
				thinks = true
			}
			if thinks && c.FinishReason == nil {
				continue
			}
			if c.FinishReason != nil {
				if r, ok := filterReasons[*c.FinishReason]; ok {
					return refusal(r)
				}
				if *c.FinishReason == "error" {
					// WorkBuddy's and a DeepSeek harness's word for a turn
					// it failed, not finished (#2471): the failure an error
					// event is, held for the group's next candidate, where
					// taken for content it went to the agent and the try
					// stood as a success
					return eventError, streamStatus(finishFailed), finishFailed
				}
				return eventContent, 0, ""
			}
		}
		if thinks {
			return eventThinking, 0, ""
		}
		return eventLead, 0, ""
	}
	if replyEvent(typ) {
		return eventContent, 0, ""
	}
	// what isn't known to carry a reply only frames it: it lets the
	// stream through no more than a ping does. Taken for content, a 200
	// that sent only such frames went through, and the overloaded error
	// after them reached Codex with five candidates never asked
	// (Aliceonly on #1418). A stream that ends well is released whole
	return eventLead, 0, ""
}

// replyEvent tells whether an event, past the cases streamEvent tells
// apart, is one of a protocol's events that carry a reply or end it:
// Anthropic's blocks and stop, Responses' text, refusal, items, parts,
// calls and audio, and its end.
func replyEvent(typ string) bool {
	switch typ {
	case "content_block_start", "content_block_delta", "message_delta", "message_stop",
		"response.completed", "response.incomplete", "response.done":
		return true
	}
	rest, ok := strings.CutPrefix(typ, "response.")
	if !ok {
		return false
	}
	for _, p := range []string{"output_text.", "refusal.", "output_item.", "content_part.", "audio", "output_audio"} {
		if strings.HasPrefix(rest, p) {
			return true
		}
	}
	// a call's arguments or a hosted tool's progress: function_call_
	// arguments, custom_tool_call_input, web_search_call, mcp_call, ...
	return strings.Contains(rest, "_call") || strings.HasPrefix(rest, "mcp_")
}

// anthropicDeltaThinks tells whether an Anthropic content_block_delta is
// reasoning with something in it.
func anthropicDeltaThinks(raw json.RawMessage) bool {
	var d struct {
		Type     string `json:"type"`
		Thinking string `json:"thinking"`
	}
	return json.Unmarshal(raw, &d) == nil && d.Type == "thinking_delta" && d.Thinking != ""
}

// anthropicDeltaSays tells whether an Anthropic content_block_delta says
// anything: text or reasoning, or a call's input. A signature doesn't.
func anthropicDeltaSays(raw json.RawMessage) bool {
	var d struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	}
	if json.Unmarshal(raw, &d) != nil {
		return true
	}
	switch d.Type {
	case "text_delta":
		return d.Text != ""
	case "thinking_delta":
		return d.Thinking != ""
	case "signature_delta":
		return false
	}
	return true
}

// errorCode is an error's code, or its type when it has none.
func errorCode(raw json.RawMessage) string {
	var e struct {
		Code any    `json:"code"`
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	if c, ok := e.Code.(string); ok && c != "" {
		return c
	}
	return e.Type
}

// policyCode is an error code that names the vendor's usage policy:
// OpenAI's bio_policy ("This content was flagged for possible biological
// risk"), cyber_policy and the like (#248).
var policyCode = regexp.MustCompile(`^[a-z]+_policy$`)

// sensitiveCode is how Volcengine Ark's errors say its filter refused the
// input or the reply: InputTextSensitiveContentDetected,
// OutputTextSensitiveContentDetected, SensitiveContentDetected and the
// image and video kinds of them.
var sensitiveCode = regexp.MustCompile(`SensitiveContentDetected$`)

// flaggedWords are how OpenAI's invalid_prompt says the prompt was held to
// its usage policy, rather than malformed.
var flaggedWords = regexp.MustCompile(`(?i)flagged|usage polic`)

// policyRefusal tells whether an error — an error object, a body holding
// one, or a stream's error event — is the vendor's safety filter refusing
// the request: Azure's content_filter, OpenAI's content_policy_violation,
// bio_policy and invalid_prompt flagged as against its usage policy. It is
// not the request at fault, as another 400 is: another account or model
// may answer it (#248).
func policyRefusal(raw []byte) (string, bool) {
	var e struct {
		Code    any    `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
		Error   *struct {
			Code    any    `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return "", false
	}
	code, typ, msg := e.Code, e.Type, e.Message
	if e.Error != nil {
		code, typ, msg = e.Error.Code, e.Error.Type, e.Error.Message
	}
	c, _ := code.(string)
	if c == "" {
		c = typ
	}
	note := func(reason string) (string, bool) {
		if msg = strings.TrimSpace(msg); msg != "" {
			// long enough for the vendor's own advice after its words:
			// Factory's "start a new session…" and OpenAI's Daybreak
			// link were cut off at 300
			if len(msg) > 1000 {
				msg = msg[:1000] + "…"
			}
			return refusedNote(reason) + " — " + msg, true
		}
		return refusedNote(reason), true
	}
	switch r, ok := filterReasons[c]; {
	case ok:
		return note(r)
	case policyCode.MatchString(c), sensitiveCode.MatchString(c):
		return note(c)
	case c == "1301" && strings.Contains(msg, "敏感"):
		// Zhipu's: "系统检测到输入或生成内容可能包含不安全或敏感内容"
		return note("sensitive")
	case c == "invalid_prompt" && flaggedWords.MatchString(msg):
		return note(c)
	case strings.TrimSpace(msg) == highRiskRefusal:
		return note("high risk")
	}
	return "", false
}

// highRiskRefusal is an error's whole message that is a refusal with no
// code beside it (#2400, Jichao: what the agent showed, the vendor not
// known). It says the request was turned away, not the account, so it goes
// to the next candidate as other refusals do and rests nobody. Only an
// error's own message is matched, and only whole: an answer quoting it is
// an answer.
const highRiskRefusal = "The request was rejected because it was considered high risk"

// filterRefused is a stream's error event (its data) as an Event carries
// it when it is the vendor's safety filter refusing the request: its code
// (refusedCode), status refusedStatus, and an error of the request's own
// (Said) with that code. A client answered whole is then told it as a 400
// refusal, which the hold reads as one (policyRefusal) and asks the next
// account, resting nobody; without the status it was answered 502
// api_error, asked again on every account, and each one rested (#3274: a
// Codex account's response.failed cyber_policy, its error with no type).
// A code policyRefusal doesn't know as a refusal goes as content_filter,
// for the hold to know it still. Not a refusal, it is "", 0 and nil.
func filterRefused(data string) (string, int, json.RawMessage) {
	code := refusedCode(data)
	if code == "" {
		return "", 0, nil
	}
	said := code
	c, _ := json.Marshal(map[string]string{"code": code})
	if _, ok := policyRefusal(c); !ok {
		said = "content_filter"
	}
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"type": "invalid_request_error", "code": said}})
	return code, refusedStatus, b
}

// refusedCode is the code of a stream's error event (its data) when it is
// the vendor's safety filter refusing, for a translation of it to keep:
// said in another protocol as only its message, ChatGPT's bio_policy read
// to Claude Code as any failure, and the account was set aside for it
// rather than the next asked as after a refusal (#248).
func refusedCode(data string) string {
	var v struct {
		Error    json.RawMessage `json:"error"`
		Response struct {
			Error json.RawMessage `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(data), &v) != nil {
		return ""
	}
	for _, raw := range []json.RawMessage{v.Response.Error, v.Error, json.RawMessage(data)} {
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		if _, ok := policyRefusal(raw); ok {
			if c := errorCode(raw); c != "" && c != "error" {
				return c
			}
			return "content_filter"
		}
	}
	return ""
}

// refusedReply tells whether a whole reply, not streamed, is the vendor's
// safety filter refusing with nothing said: Anthropic's stop_reason
// "refusal", Chat's finish_reason "content_filter", Responses' incomplete
// for content_filter, Gemini's SAFETY — with no text or call in it.
func refusedReply(b []byte) (string, bool) {
	var v struct {
		// Anthropic's
		StopReason  string          `json:"stop_reason"`
		StopDetails json.RawMessage `json:"stop_details"`
		Content     []streamPart    `json:"content"`
		// Chat's
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   any   `json:"content"`
				ToolCalls []any `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		// Responses'
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []streamPart `json:"output"`
		// Gemini's
		Candidates []struct {
			Content struct {
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	if json.Unmarshal(b, &v) != nil {
		return "", false
	}
	none := func(ps []streamPart) bool {
		return !slices.ContainsFunc(ps, streamPart.said)
	}
	if r, ok := filterReasons[v.StopReason]; ok && r == "refusal" && none(v.Content) {
		return refusedDetail(r, v.StopDetails), true
	}
	for _, c := range v.Choices {
		if r, ok := filterReasons[c.FinishReason]; ok && (c.Message.Content == nil || c.Message.Content == "") && len(c.Message.ToolCalls) == 0 {
			return refusedNote(r), true
		}
	}
	if d := v.IncompleteDetails; v.Status == "incomplete" && d != nil && none(v.Output) {
		if r, ok := filterReasons[d.Reason]; ok {
			return refusedNote(r), true
		}
	}
	if r, ok := filterReasons[v.PromptFeedback.BlockReason]; ok {
		return refusedNote(r), true
	}
	for _, c := range v.Candidates {
		if r, ok := filterReasons[c.FinishReason]; ok && len(c.Content.Parts) == 0 {
			return refusedNote(r), true
		}
	}
	return "", false
}

// errorReply says whether a reply that came whole under a success status
// is only an error: OpenAI's or Gemini's {"error":{…}}, Anthropic's
// {"type":"error","error":{…}}, a Responses object whose error is set —
// with no reply beside it. msg is what it says (provider.APIError), and
// status what it stands for (streamStatus), as the same error said in a
// stream is read: a relay that answers OpenAI's TPM limit as a 200 fails
// as its 429 would, asked again in a moment, not as a reply that went
// through (monater on Discord).
func errorReply(body []byte) (status int, msg string, ok bool) {
	b := bytes.TrimSpace(body)
	if len(b) == 0 || b[0] != '{' {
		return 0, "", false
	}
	var v struct {
		Error      json.RawMessage `json:"error"`
		Choices    []any           `json:"choices"`
		Content    []any           `json:"content"`
		Candidates []any           `json:"candidates"`
		Output     []any           `json:"output"`
		Status     string          `json:"status"`
	}
	if json.Unmarshal(b, &v) != nil || !carries(v.Error) || len(v.Choices)+len(v.Content)+len(v.Candidates) > 0 ||
		len(v.Output) > 0 && v.Status != "failed" {
		return 0, "", false
	}
	var e struct {
		Message any `json:"message"`
		Type    any `json:"type"`
		Code    any `json:"code"`
		Status  any `json:"status"`
	}
	var said string
	if json.Unmarshal(v.Error, &said) == nil {
		if strings.TrimSpace(said) == "" {
			return 0, "", false
		}
	} else if json.Unmarshal(v.Error, &e) != nil || e.Message == nil && e.Type == nil && e.Code == nil && e.Status == nil {
		return 0, "", false
	}
	if said := namedStatusOf(v.Error); said != 0 {
		// the error names its own status (#4855)
		return said, provider.APIError(b, "the upstream reported an error"), true
	}
	return streamStatus(string(wordsOf(b))), provider.APIError(b, "the upstream reported an error"), true
}

// namedStatus is the status an error carried under a 200 names for
// itself (#4855): its error object's own (namedStatusOf), or, as words
// alone (a reply's error text), the status they name (textStatus). 0
// when it names none.
func namedStatus(body []byte) int {
	var v struct {
		Error json.RawMessage `json:"error"`
	}
	if b := bytes.TrimSpace(body); len(b) > 0 && b[0] == '{' {
		if json.Unmarshal(b, &v) == nil && carries(v.Error) {
			return namedStatusOf(v.Error)
		}
		return 0
	}
	return textStatus(string(body))
}

// namedStatusOf is the status an error object names for itself (#4855):
// a plugin that ends an OpenAI-compatible stream after its 200 can only
// say what went wrong in the frame, and the status it names there is the
// routing signal. A number 400–599 on a member of its own, status first,
// then code as Gemini's relays carry it (a string code is not a status);
// else the status its message names in words (textStatus). 0 when it
// names none, when its words say the request is too long (its overflow is
// read from the words, wherever it is said), and for magpie's own words,
// which a magpie relayed to says too (magpieSaid).
func namedStatusOf(errRaw []byte) int {
	if !carries(errRaw) {
		return 0
	}
	var e struct {
		Code    json.RawMessage `json:"code"`
		Status  json.RawMessage `json:"status"`
		Message json.RawMessage `json:"message"`
	}
	var msg string
	if json.Unmarshal(errRaw, &msg) != nil {
		if json.Unmarshal(errRaw, &e) != nil {
			return 0
		}
		json.Unmarshal(e.Message, &msg)
	}
	if magpieSaid(msg) || tooLong(http.StatusBadRequest, msg) {
		return 0
	}
	num := func(raw json.RawMessage) int {
		// a string code ("429", Zhipu's "1113") is the vendor's own
		// code, not a status: json.Number would take a quoted number
		var n json.Number
		if len(raw) == 0 || raw[0] < '0' || raw[0] > '9' || json.Unmarshal(raw, &n) != nil {
			return 0
		}
		if i, err := n.Int64(); err == nil && i >= 400 && i < 600 {
			return int(i)
		}
		return 0
	}
	for _, raw := range []json.RawMessage{e.Status, e.Code} {
		if n := num(raw); n != 0 {
			return n
		}
	}
	return textStatus(msg)
}

// httpSaid is a status an error's words name as one, "(HTTP 503)" or
// "（HTTP 503）": a plugin that can't fail its 200 any more puts its
// upstream's status in its message (mycf_711's wps-lingxi rows,
// "灵犀上游返回 AIError：AI服务异常（上游暂时不可用，可重试）（HTTP 503）").
var httpSaid = regexp.MustCompile(`[(（]\s*HTTP\s*(429|5\d\d)\s*[)）]`)

// textStatus is the status an error's words name (httpSaid), 0 when they
// name none, or say the request is too long, or are magpie's own. Only a
// rate limit or a server's failure is read so: a 4xx a message quotes may
// be about anything it fetched, not the request.
func textStatus(msg string) int {
	if magpieSaid(msg) || tooLong(http.StatusBadRequest, msg) {
		return 0
	}
	m := httpSaid.FindStringSubmatch(msg)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// magpieSaid says an error is in magpie's own words, not an upstream's:
// its loop guard ending a reply, an empty reply, a reasoning loop asked
// again. Said by a magpie this one relays to (a LAN peer) too, they are
// no failure of the account that answered (#4855).
func magpieSaid(msg string) bool {
	return strings.Contains(msg, loopGuardSaid) || strings.Contains(msg, emptyReply) || strings.Contains(msg, reloopedError)
}

// streamStatus is the status an error in a stream stands for, as a vendor
// would have answered it before streaming.
func streamStatus(msg string) int {
	l := strings.ToLower(msg)
	switch {
	case tooLong(http.StatusBadRequest, msg):
		// before the quota words: the ChatGPT backend's response.failed
		// for a Codex conversation past the model's window says
		// "context_length_exceeded", whose "exceeded" read as a rate
		// limit, and the same request was sent again and again (#1944)
		return 400
	case strings.Contains(l, "overloaded"):
		return 529
	case creditWords.MatchString(msg) && !strings.Contains(l, "rate"):
		return 402
	case strings.Contains(l, "rate_limit"), strings.Contains(l, "rate limit"), strings.Contains(l, "too many"), quotaSaid(msg):
		return 429
	case unservedWords.MatchString(msg):
		return 400
	case strings.Contains(l, "invalid"), strings.Contains(l, "context_length"), strings.Contains(l, "too long"):
		return 400
	}
	return 502
}

// pinTo is cands narrowed to the account a request names in AccountHeader
// (its user, or its id in the routing trace), and pl with them. Nothing
// else is tried in its place: a caller that names one account asks about
// that one. The error says why none is left: no such account, one that
// doesn't list the model, or one resting. A name that isn't ASCII comes
// percent-encoded (pinnedName), and matches as written or decoded.
func pinTo(raw string, cands []candidate, pl planned) ([]candidate, planned, int, string) {
	want := pinnedName(raw)
	match := func(w Weighed) bool {
		return strings.EqualFold(want, w.Who) || want == w.ID || strings.EqualFold(raw, w.Who) || raw == w.ID
	}
	var out []candidate
	var order []Weighed
	var rests []string
	for i, c := range cands {
		if i >= len(pl.order) || !match(pl.order[i]) {
			continue
		}
		r, ok := restOf(c.restKey())
		if !ok && c.restID() != c.restKey() {
			r, ok = restOf(c.restID())
		}
		if ok {
			rests = append(rests, fmt.Sprintf("%s rests until %s (%s)", c.label(), r.Until.Format(time.RFC3339), r.Why))
			continue
		}
		out, order = append(out, c), append(order, pl.order[i])
	}
	if len(out) > 0 {
		return out, planned{order: order}, 0, ""
	}
	if len(rests) > 0 {
		return nil, pl, http.StatusTooManyRequests, AccountHeader + ": " + strings.Join(rests, "; ") + "; no other account is tried in its place"
	}
	for _, w := range pl.left {
		if match(w) && w.Held {
			return nil, pl, http.StatusForbidden, fmt.Sprintf("%s: the gateway key may not use %s's account", AccountHeader, w.Who)
		}
		if match(w) && w.Capped > 0 {
			msg, _ := cappedError(w.Model, []Weighed{w}, time.Now())
			return nil, pl, http.StatusTooManyRequests, AccountHeader + ": " + msg + "; no other account is tried in its place"
		}
		if match(w) {
			return nil, pl, http.StatusBadRequest, fmt.Sprintf("%s: %s's plan doesn't list %s", AccountHeader, w.Who, w.Model)
		}
	}
	var have []string
	for _, w := range append(slices.Clone(pl.order), pl.left...) {
		if w.Kind == "account" && !slices.Contains(have, w.Who) {
			have = append(have, w.Who)
		}
	}
	msg := fmt.Sprintf("%s: no account %q serves this model", AccountHeader, want)
	if len(have) > 0 {
		msg += "; its accounts are " + strings.Join(have, ", ")
	}
	return nil, pl, http.StatusNotFound, msg
}

// pinnedName is the account AccountHeader names, raw as the request sent
// it. A header value is ASCII on the wire, and fetch refuses to send one
// that isn't, so a user named in other letters (北冥, plugins#615) is sent
// percent-encoded (encodeURIComponent): it is read back decoded. A value
// that isn't a valid encoding of UTF-8 is the name as written.
func pinnedName(raw string) string {
	if !strings.Contains(raw, "%") {
		return raw
	}
	if d, err := url.PathUnescape(raw); err == nil && utf8.ValidString(d) {
		return strings.TrimSpace(d)
	}
	return raw
}

// unservedTry says c's try failed as the vendor refusing the model as one
// it doesn't serve the account or key (modelRefused, codeAssistModelMissing):
// what restAfterMarked rests for that model alone.
func unservedTry(c candidate, status int, body []byte) bool {
	return failure(status, body) == failOther && (modelRefused(status, body) || codeAssistModelMissing(c.p, status, body))
}

// unlistedNext takes out of a plan's unlisted accounts (planned.unlisted,
// their rows in ws) those of key, "<provider>/<model>", not resting for the
// model: the next ones asked once the accounts whose own list has the model
// refused it as one they don't serve. The rest are returned too, for
// none to be asked twice.
func unlistedNext(cs []candidate, ws []Weighed, key string) (next []candidate, nextW []Weighed, restCs []candidate, restWs []Weighed) {
	for i, c := range cs {
		_, resting := restingOf(c)
		if c.p.ID+"/"+c.model != key || resting {
			restCs = append(restCs, c)
			if i < len(ws) {
				restWs = append(restWs, ws[i])
			}
			continue
		}
		next = append(next, c)
		if i < len(ws) {
			w := ws[i]
			w.Unlisted = false // asked, after those that list the model
			nextW = append(nextW, w)
		}
	}
	return next, nextW, restCs, restWs
}

// askUnlisted moves the rows of the unlisted accounts asked next (ws) from
// a route's left to its order, after the row of c, the one that refused
// the model: the Routing page has them asked, not "unlisted, skipped".
func askUnlisted(order, left []Weighed, c candidate, ws []Weighed) ([]Weighed, []Weighed) {
	asked := make(map[string]bool, len(ws))
	for _, w := range ws {
		asked[w.ID+"/"+w.Model] = true
	}
	left = slices.DeleteFunc(slices.Clone(left), func(w Weighed) bool { return w.Unlisted && asked[w.ID+"/"+w.Model] })
	at := slices.IndexFunc(order, func(w Weighed) bool { return w.ID == c.rest && w.Model == c.model })
	if at < 0 {
		return append(slices.Clip(order), ws...), left
	}
	return slices.Concat(order[:at+1], ws, order[at+1:]), left
}
