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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const fallbackCooldown = time.Minute

type candidate struct {
	p     provider.Provider
	model string
	rest  string // what rests after a failure: the provider, or one of its keys
	// effort is the reasoning the group's member it is of is fixed at
	// ("provider/model:low"); "" for one that follows the agent or the group
	effort string
	// fast is set on a member the group sends in its vendor's fast mode
	// (Group.Fast)
	fast bool
	// rank is its place in its provider's own list of accounts or keys,
	// the order the provider's page shows and a drag sets (#217)
	rank int
	// capped is set on an account left out as held at its usage cap
	capped *capHold
}

// capHold is how an account is held at its usage cap: the cap, the share
// of the fullest window at or past it, and when the last such window
// renews (zero when one doesn't say). noCredits: it is a Codex account
// held at 100% as it is set not to spend its credits (cap is 100 then).
type capHold struct {
	cap       int
	used      float64
	back      time.Time
	noCredits bool
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
	return provider.WithMemberEffort(c.rest+"/"+c.model, c.effort)
}

func (c candidate) isOpenRouterFree() bool {
	return c.p.Preset == "openrouter" && strings.HasSuffix(c.model, ":free")
}

// restID is the candidate's model's own rest key: a free OpenRouter
// model's, whose rate limit is its free-tier limit, and a subscription's,
// out of a pool of its allowance that counts some models only (pooled).
// An account-level rest stays on restKey.
func (c candidate) restID() string {
	if c.isOpenRouterFree() || c.p.Account != nil {
		return c.restKey() + "/" + c.model
	}
	return c.restKey()
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
	if p.Account != nil {
		also := p.AlsoOn()
		var all []candidate
		// the account the agent is signed in to, unless the user paused
		// it for the others on (#263)
		if len(also) == 0 || !p.OwnPaused() {
			all = append(all, candidate{p: p, model: model, rest: p.ID})
		}
		for i, q := range also {
			all = append(all, candidate{p: q, model: model, rest: p.ID + "@" + q.Account.User, rank: i + 1})
		}
		// kept signed in to an account of the user's choosing, the one
		// signed in to stands at its own place in the order, not first
		// (#524)
		if ranks := p.LoginRanks(); ranks != nil {
			at := func(c candidate) int {
				if r, ok := ranks[strings.ToLower(c.p.Account.User)]; ok {
					return r
				}
				return len(ranks)
			}
			slices.SortStableFunc(all, func(a, b candidate) int { return at(a) - at(b) })
			for i := range all {
				all[i].rank = i
			}
		}
		all = slices.DeleteFunc(all, func(c candidate) bool {
			if p.AccountServes(c.p.Account.User, model) {
				return false
			}
			barred = append(barred, c)
			return true
		})
		// an account at its usage cap is used up for routing until the
		// window it filled renews: never tried, so groups, fallbacks and
		// the other accounts take the request (provider/account_caps.go);
		// so is a Codex account at 100% set not to spend its credits
		// (provider/codex_credits.go)
		all = slices.DeleteFunc(all, func(c candidate) bool {
			h := capHeld(p, c.p, model, time.Now())
			if h == nil {
				return false
			}
			c.capped = h
			barred = append(barred, c)
			return true
		})
		// an account whose plan lacks the model (a Free one behind a Plus)
		// would only answer 400; it is tried only when none lists it
		for _, c := range all {
			if c.p.Account.Lists(model) {
				out = append(out, c)
			} else {
				left = append(left, c)
			}
		}
		if len(out) == 0 {
			return all, nil, nil, barred
		}
		return out, nil, left, barred
	}
	keys := p.KeysOn()
	var unlisted []candidate
	for i, k := range keys {
		q := p.WithKey(k)
		if len(q.Speaks()) == 0 {
			continue // made for a protocol this provider has no endpoint for
		}
		rest := p.ID
		if len(keys) > 1 {
			rest += "#" + provider.KeyID(k.Key)
		}
		if !p.AccountServes(provider.KeyID(k.Key), model) {
			barred = append(barred, candidate{p: q, model: model, rest: rest, rank: i})
			continue
		}
		if !p.Serves(k, model) {
			// the vendor lists the model to another key only
			unlisted = append(unlisted, candidate{p: q, model: model, rest: rest, rank: i})
			continue
		}
		out = append(out, candidate{p: q, model: model, rest: rest, rank: i})
	}
	if len(out) == 0 {
		out, unlisted = unlisted, nil // no key lists it: try them all the same
	}
	if len(out) == 0 && len(barred) > 0 {
		return nil, nil, nil, barred
	}
	if len(out) == 0 {
		return []candidate{{p: p, model: model, rest: p.ID}}, nil, nil, nil
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
	return pool, aside, unlisted, barred
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
		if q.KeyProtocol != provider.Anthropic {
			return 0
		}
		return 2
	}
	if q.Base(from) != "" {
		return 0
	}
	return 1
}

// modelFamily is the protocol a model is at home in, when its name says:
// Anthropic for Claude, Chat (standing for OpenAI's) for GPT and the o-series.
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
	var pl planned
	add := func(q provider.Provider, m string, fallback bool) []candidate {
		cs, aside, left, barred := perKeyBarred(q, m, from)
		pl.left = append(pl.left, barredOf(barred, q, fallback, from, nil)...)
		pl.held = append(pl.held, creditsHeld(barred)...)
		cs, wg := weigh(q, cs, m, from)
		for i, c := range cs {
			w := weighed(c, q, wg, fallback, from)
			w.Turn = i == 0 && (q.Routing == provider.Rotate || q.Routing == provider.Weighted) && len(cs) > 1
			pl.order = append(pl.order, w)
		}
		pl.order = append(pl.order, asideOf(aside, q, fallback, from)...)
		cs = append(cs, aside...)
		for _, c := range left {
			w := weighed(c, q, weighing{}, fallback, from)
			w.Unlisted = true
			pl.left = append(pl.left, w)
		}
		return cs
	}
	out := add(p, model, false)
	seen := map[string]bool{p.ID + "/" + model: true}
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok || seen[fp.ID+"/"+fm] {
			continue
		}
		seen[fp.ID+"/"+fm] = true
		out = append(out, add(fp, fm, true)...)
	}
	return restLast(out, pl)
}

// planGroup is plan for a routing group: every member's keys or accounts
// weighed together as the group's routing says — in order, member by
// member, each as its own provider's routing orders it; else all as one,
// so a subscription whose allowance renews soonest goes first whichever
// provider it is of. A group in the group is planned by its own routing
// alone, whatever the group's (#576): in order, in its place; else as
// one, weighed with the rest by the one it would try first, and tried
// whole where that one goes — the group's routing picks between its
// groups, never within them. A member's fallbacks are not the group's.
func (s *Server) planGroup(g provider.Group, ms []provider.Member, from provider.Protocol) ([]candidate, planned) {
	var pl planned
	var asides []candidate
	var wAsides []Weighed
	out := planLevel(g, ms, 0, from, &pl, &asides, &wAsides)
	if sinks(g.Sink, g.Routing) {
		out, pl.order = sinkPlanned(out, pl.order)
	}
	out, pl.order = append(out, asides...), append(pl.order, wAsides...)
	if len(out) == 0 {
		return nil, pl
	}
	return restLast(out, pl)
}

// planLevel orders the models ms of g, a group depth groups down from the
// one asked for, adding to pl's order as it goes: those set aside and
// those unlisted it gathers for planGroup to put last.
func planLevel(g provider.Group, ms []provider.Member, depth int, from provider.Protocol, pl *planned, asides *[]candidate, wAsides *[]Weighed) []candidate {
	keys := func(m provider.Member) []candidate {
		cs, aside, left, barred := perKeyBarred(m.Provider, m.Model, from)
		pl.left = append(pl.left, barredOf(barred, m.Provider, false, from, m.Groups())...)
		pl.held = append(pl.held, creditsHeld(barred)...)
		// the effort the member is fixed at goes with each of its keys:
		// the same model at another effort is another member's
		for _, l := range [][]candidate{cs, aside, left} {
			for i := range l {
				l[i].effort, l[i].fast = m.Effort, m.Fast
			}
		}
		*asides = append(*asides, aside...)
		for _, w := range asideOf(aside, m.Provider, false, from) {
			w.Via = m.Groups()
			*wAsides = append(*wAsides, w)
		}
		for _, c := range left {
			w := weighed(c, m.Provider, weighing{}, false, from)
			w.Unlisted, w.Via = true, m.Groups()
			pl.left = append(pl.left, w)
		}
		return cs
	}
	if g.Routing != provider.Ordered {
		// what the group weighs: each of its models' keys or accounts,
		// and each group in it as one — planned by its own routing, put
		// where the one it would try first is, the rest of it after
		type unit struct {
			m     provider.Member // the model, for one of its keys
			cs    []candidate     // a group in g: its own order
			order []Weighed
		}
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
				cs := planLevel(m.Via[depth], ms[i:j], depth+1, from, &sub, asides, wAsides)
				pl.left = append(pl.left, sub.left...)
				if len(cs) > 0 {
					// weighed by the first it would try that isn't resting
					head := cs[0]
					for _, c := range cs {
						if _, resting := restOf(c.restKey()); !resting {
							if _, resting = restOf(c.restID()); !resting {
								head = c
								break
							}
						}
					}
					add(unit{cs: cs, order: sub.order}, head)
				}
				i = j
				continue
			}
			for _, c := range keys(m) {
				add(unit{m: m}, c)
			}
			i++
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
			w.Turn = i == 0 && g.Routing == provider.Rotate && len(weighedHeads) > 1
			pl.order = append(pl.order, w)
			out = append(out, c)
		}
		return out
	}
	var out []candidate
	for i := 0; i < len(ms); {
		m := ms[i]
		if len(m.Path) > depth+1 {
			// a group in g: its models, planned by its routing
			j := i + 1
			for j < len(ms) && len(ms[j].Path) > depth+1 && ms[j].Path[depth] == m.Path[depth] {
				j++
			}
			out = append(out, planLevel(m.Via[depth], ms[i:j], depth+1, from, pl, asides, wAsides)...)
			i = j
			continue
		}
		cs, wg := weigh(m.Provider, keys(m), m.Model, from)
		for k, c := range cs {
			w := weighed(c, m.Provider, wg, false, from)
			w.Turn = k == 0 && (m.Provider.Routing == provider.Rotate || m.Provider.Routing == provider.Weighted) && len(cs) > 1
			w.Via = m.Groups()
			pl.order = append(pl.order, w)
		}
		out = append(out, cs...)
		i++
	}
	return out
}

// capHeld is how the account acct of p is held at its usage cap for
// model at now, or at 100% as a Codex account set not to spend its
// credits; nil when nothing holds it or it is below the share. Its windows
// are those last read (allowances), as smart routing weighs them.
func capHeld(p, acct provider.Provider, model string, now time.Time) *capHold {
	if acct.Account == nil {
		return nil
	}
	a := acct.Account
	share, noCredits := provider.HoldShare(p, a.Agent, a.User)
	if share <= 0 {
		return nil
	}
	held, used, back := allowances(a.UsageAgent())[a.User].CapHeld(model, share, now)
	if !held {
		return nil
	}
	return &capHold{cap: share, used: used, back: back, noCredits: noCredits}
}

// cappedError says why a request for model went nowhere when every account
// that could take it is held at its usage cap, as the vendor's own refusal
// would when all are used up; the soonest one is back is the wait.
func cappedError(model string, ws []Weighed, now time.Time) (string, time.Time) {
	var held []string
	var soonest time.Time
	capped, noCredits := heldBy(ws)
	for _, w := range ws {
		if w.Capped == 0 {
			continue
		}
		s := fmt.Sprintf("%s (%s) is at %.0f%% of a usage window, past its %d%% cap", w.Name, w.Who, w.Used, w.Capped)
		if w.NoCredits {
			s = fmt.Sprintf("%s (%s) has used up a usage window and is set not to spend its credits", w.Name, w.Who)
		}
		if w.CapBack != nil {
			s += ", until " + w.CapBack.Local().Format("Jan 2 15:04")
			if soonest.IsZero() || w.CapBack.Before(soonest) {
				soonest = *w.CapBack
			}
		}
		held = append(held, s)
	}
	switch {
	case !noCredits:
		return fmt.Sprintf("usage cap reached: every account that serves %q is held at the usage cap set on it in magpie — %s. magpie uses it again once that window renews; raise or lift the cap in magpie (Providers → the account's cap, or magpie provider account-cap)", model, strings.Join(held, "; ")), soonest
	case !capped:
		return fmt.Sprintf("usage limit reached: every account that serves %q has used up its allowance and is set in magpie not to spend its credits — %s. magpie uses it again once that window renews; to go on now, if an account holds credits, let it spend them (Usage → the account's Use credits, or magpie quota credits <account> on)", model, strings.Join(held, "; ")), soonest
	}
	return fmt.Sprintf("usage limit reached: every account that serves %q is held by magpie — %s. magpie uses it again once that window renews; to go on now, raise or lift a cap (Providers → the account's cap, or magpie provider account-cap) or, if an account holds credits, let it spend them (Usage → the account's Use credits, or magpie quota credits <account> on)", model, strings.Join(held, "; ")), soonest
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

// cappedRecord is the call's record of the same refusal as cappedError,
// told apart the same way.
func cappedRecord(ws []Weighed) string {
	switch capped, noCredits := heldBy(ws); {
	case !noCredits:
		return "every account at its usage cap"
	case !capped:
		return "every account held: set not to spend its credits"
	}
	return "every account held: at its usage cap or set not to spend its credits"
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

// barredOf is how the trace tells the accounts or keys the user set not
// to serve the model: left out, as those not listing it are.
func barredOf(cs []candidate, q provider.Provider, fallback bool, from provider.Protocol, via []string) []Weighed {
	var out []Weighed
	for _, c := range cs {
		w := weighed(c, q, weighing{}, fallback, from)
		w.Unlisted, w.Via = true, via
		if h := c.capped; h != nil {
			w.Capped, w.Used, w.NoCredits = h.cap, h.used, h.noCredits
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

// restLast moves those resting after a recent failure behind the rest.
func restLast(out []candidate, pl planned) ([]candidate, planned) {
	if len(out) == 1 {
		if r, ok := restOf(out[0].restKey()); ok {
			pl.order[0].Rest = &r // tried all the same: there is no other
		} else if r, ok := restOf(out[0].restID()); ok {
			pl.order[0].Rest = &r // tried all the same: there is no other
		}
		return out, pl
	}
	var ready, resting []candidate
	var wReady, wResting []Weighed
	for i, c := range out {
		r, ok := restOf(c.restKey())
		if !ok && c.restID() != c.restKey() {
			r, ok = restOf(c.restID())
		}
		if ok {
			pl.order[i].Rest = &r
			resting, wResting = append(resting, c), append(wResting, pl.order[i])
		} else {
			ready, wReady = append(ready, c), append(wReady, pl.order[i])
		}
	}
	pl.order = append(wReady, wResting...)
	return append(ready, resting...), pl
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

var restingUntil = struct {
	sync.Mutex
	m    map[string]time.Time
	note map[string]Rest // why each rests
}{m: map[string]time.Time{}, note: map[string]Rest{}}

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
var quotaWords = regexp.MustCompile(`(?i)quota|insufficient|balance|credit|billing|out of budget|budget (exceeded|exhausted)|exceeded|rate.?limit|usage.?limit|limit.?reached|hit your .*limit|limit.{0,24}resets|too many requests|overloaded|余额|额度|欠费|限流|频率|套餐|用量|上限`)

// unservedWords are how a vendor says the model isn't one it serves this
// key, or this way — words another provider, or key, may not answer with.
var unservedWords = regexp.MustCompile(`(?i)model.{0,80}(not (supported|accessible|available|found|enabled|allowed)|unsupported|does ?n[o']t exist|unknown|invalid)|(no such|unknown|invalid|unsupported) model|model_not_found|模型.{0,12}(不存在|不支持|无权|未开通)`)

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
// (#350), OpenAI's "Unknown parameter". A request missing what every API
// requires ("field required") or too long for the model isn't one.
var shapeWords = regexp.MustCompile(`(?i)failed to deserialize|unknown (item |content |input )?(type|variant|field|parameter)|unknown_parameter|unrecognized (request argument|field|parameter)|extra (inputs|fields) are not permitted|additional properties are not allowed`)

// shapeRefused says a request failed over its shape alone: the next
// member is asked, and this one doesn't rest, as nothing is wrong with it.
func shapeRefused(status int, body []byte) bool {
	return (status == 400 || status == 422) && shapeWords.Match(body) &&
		!quotaWords.Match(body) && !unservedWords.Match(body) && !refusedWords.Match(body)
}

// retryable says whether another provider may do better with a request
// that failed this way: the vendor was busy, out of quota or failing, or
// this key or provider can't serve it — not the request itself at fault.
func retryable(status int, body []byte) bool {
	switch {
	case overflowed(status, body):
		// a conversation too long for the model is as long on every
		// account of it: the agent is told, and compacts
		return false
	case status == 401, status == 402, status == 403, status == 404, status == 408, status == 429, status >= 500:
		return true
	case status >= 400 && provider.EdgeBlocked(body):
		// the vendor's firewall blocked this address (Alibaba Cloud's 405
		// in front of zcode.z.ai): another provider goes another way
		return true
	case status == 400, status == 422:
		return quotaWords.Match(body) || unservedWords.Match(body) || refusedWords.Match(body) || shapeWords.Match(body)
	}
	return false
}

// lateRests says whether a reply that broke off after it began, with this
// error, was the vendor failing (#733): unreachable, overloaded, out of
// quota. A conversation grown too long for the model, or a request the
// vendor turned away as it reads, would fail the same at the next asked:
// nobody rests for it.
func lateRests(msg string) bool {
	return !tooLong(http.StatusBadRequest, msg) && !refusedWords.MatchString(msg)
}

// unsaidMargin is how far past a model's window a request's estimate
// must go to be taken as too long for it: estimate counts a token as four
// bytes, which most text runs under (Devin counted 173,954 tokens of one
// estimated at 151,851), so one past it by a fifth hardly fits.
const unsaidMargin = 1.2

// tooLongUnsaid says a failure that doesn't say why was the request being too
// long for the model, as some vendors put it: Devin's 502 "capacity
// issues", WorkBuddy's 400 "Invalid request parameters", Qoder's 400
// "Error in upstream response" — each for a conversation well past the
// model's window, and answered at a size it holds. Retried, or asked of
// another account of the model, it fails the same, and the agent, told it
// as a vendor's error, never compacts. The message it is told instead
// says the prompt is too long. Not for a rate limit, an account's
// trouble, a model it doesn't serve, or one already saying it overflowed.
func tooLongUnsaid(p provider.Provider, model string, tokens, status int, body []byte) (string, bool) {
	switch status {
	case 400, 413, 422, 500, 502, 503, 504:
	default:
		return "", false
	}
	if overflowed(status, body) || failure(status, body) != failOther || quotaWords.Match(body) ||
		unservedWords.Match(body) || refusedWords.Match(body) || provider.EdgeBlocked(body) {
		return "", false
	}
	window := windowOf(p, model)
	if window <= 0 || float64(tokens) < float64(window)*unsaidMargin {
		return "", false
	}
	msg := fmt.Sprintf("prompt is too long: about %d tokens, more than %s's context window of %d", tokens, model, window)
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

// windowOf is the tokens a provider's model takes, where known: the
// user's (Provider.ContextOf), else its catalog entry's.
func windowOf(p provider.Provider, model string) int {
	if n := p.ContextOf(model); n > 0 {
		return n
	}
	for _, e := range provider.Served() {
		if e.Group == "" && e.Provider.ID == p.ID && (e.Model == model || e.ID == model) && e.Context > 0 {
			return e.Context
		}
	}
	return 0
}

const (
	// lastRetries is how many times the last one left is tried again after
	// a failure that passes — a busy vendor, a dropped connection.
	lastRetries = 2
	// rateRetries is as many for a rate limit, which takes longer to
	// clear than a busy moment: pauses of 1, 2 and 4s, or what Retry-After
	// says, under half a minute in all
	rateRetries = 3
	// longestPause is the longest the vendor's Retry-After is waited for
	// before that; longer, and the agent gets the error.
	longestPause = 8 * time.Second
)

// retryPause is the first pause before the last one left is tried again;
// each pause after is twice the one before.
var retryPause = time.Second

// passing says whether a failure is one that may be gone a moment later,
// and how long to wait before trying the same one again, the again'th time.
func passing(status int, header http.Header, body []byte, again int) (time.Duration, bool) {
	wait := retryPause << again
	if d := retryAfter(header, time.Now()); d > 0 {
		wait = d
	}
	switch {
	case wait > longestPause:
		return 0, false
	case status == 408, status == 500, status == 502, status == 503, status == 504, status == 529:
		return wait, again < lastRetries
	case status == 429:
		// a relay's 429 often says nothing of when (#503: "负载已饱和，请稍
		// 后再试"); a plan used up or no money left won't clear in seconds
		return wait, again < rateRetries && failure(status, body) == failRate && !creditWords.Match(body)
	}
	return 0, false
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
		if w := windowOf(x.p, x.model); w > 0 && tokens >= w {
			continue
		}
		out = append(out, x)
	}
	return out
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

	// refused: the vendor's safety filter ended the reply before any of it
	// was said — Anthropic's stop_reason "refusal", OpenAI's content_filter
	// — which another account or model may answer (#248)
	refused bool
	whole   bool // a reply that isn't streamed, held whole until release

	// buffered: the vendor said it holds the reply back for safety checks,
	// which may end in a refusal: held longer (holdBuffered)
	buffered bool
	// thinking: the reply has reasoned but said nothing yet, which a
	// refusal may still end: held longer (holdThinking)
	thinking bool
	// thinkingShown: the model's vendor never refuses after reasoning
	// (refusesAfterThinking), so its reasoning goes through as it comes
	thinkingShown bool

	ended bool   // the stream's last event was written: the reply is whole
	tail  []byte // the end of the last write, for a marker split across two
	// ctx is the agent's request: a stream's last event written once it
	// has gone reached nobody, and doesn't make the reply whole (#751)
	ctx context.Context

	// alive is the request's, shared by its tries: a stream held for its
	// reasoning keeps the agent from its idle timeout with SSE comments
	// meanwhile (#751)
	alive *keptAlive

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
}

// errSlowStart is what a try's writes get once it was let go for taking
// longer than its group waits for a first token.
var errSlowStart = errors.New("let go: no first token in time")

func newHoldWriter(w http.ResponseWriter, hold bool) *holdWriter {
	return &holdWriter{w: w, hold: hold, header: http.Header{}, first: firstToken{start: time.Now()}}
}

func (h *holdWriter) Header() http.Header { return h.header }

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
		if k != resetsHeader && k != refusedHeader { // magpie's own notes, for restAfter and settle
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
	if h.status == 0 {
		h.writeHeader(http.StatusOK)
	}
	h.see(b)
	h.first.see(b)
	h.heard = time.Now()
	if h.passing {
		h.sent(b)
		return h.w.Write(b)
	}
	n, err := h.held.Write(b)
	if h.stream && h.failure == 0 {
		h.scan()
	}
	return n, err
}

// streamEnds are how each protocol's stream says it is over, as they can
// only appear outside a quoted string: text that says so is escaped.
var streamEnds = [][]byte{
	[]byte(`"type":"response.completed"`), []byte(`"type":"response.incomplete"`), // Responses
	[]byte(`"type":"message_stop"`), []byte("event: message_stop"), // Anthropic
	[]byte("data: [DONE]"), // Chat Completions
}

// see notes a stream's last event going by. An agent may hang up as soon as
// it has that — Codex does — and a reply it had whole is not canceled.
func (h *holdWriter) see(b []byte) {
	if h.ended || h.ctx != nil && h.ctx.Err() != nil {
		return
	}
	buf := append(h.tail, b...)
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

// refusesAfterThinking tells whether a model's vendor may end a reply that
// has only reasoned with its safety filter's refusal: Claude's stop_reason
// refusal, OpenAI's content_filter or bio_policy, Gemini's SAFETY (#248).
// Anybody else's reasoning — GLM's, DeepSeek's, Kimi's… — is shown as it
// comes: held, GLM on a ZCode account through a group showed Claude Code
// its thinking only once the text began, all at once (悠悠哥 on Discord).
func refusesAfterThinking(model string) bool {
	if modelFamily(model) != "" {
		return true
	}
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.HasPrefix(m, "gemini")
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
			if h.thinkingShown {
				h.flow()
				return
			}
			h.thinking = true
			continue
		case eventError:
			h.failure, h.failMsg = status, msg
			if h.stop != nil {
				h.stop()
			}
			return
		case eventRefusal:
			h.failure, h.failMsg, h.refused = status, msg, true
			return
		}
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
	if (h.thinking || h.buffered || waiting) && time.Since(h.since) >= keepHeldAfter {
		h.keepAlive()
	}
}

// keepHeldAfter is how long a stream held for its reasoning waits before
// its agent is first kept alive, as long as a stream with nothing in it
// is held (holdLongest); a refusal after it still goes to another account,
// whose stream follows the comments in the same response.
var keepHeldAfter = holdLongest

// keptAlive is what a request's held streams have sent its agent ahead of
// a reply: the stream's headers and SSE comments, nothing of the reply.
type keptAlive struct {
	proto provider.Protocol
	sent  bool      // the stream's 200 and headers went out
	at    time.Time // the last comment
}

// keepAlive tells the agent of a stream held past keepHeldAfter for its
// reasoning that it goes on, every keepaliveEvery: held for up to
// holdThinking (4 minutes), the agent heard nothing at all, and Alma's
// idle timeout for a reasoning model, 240s, ended the turn first (#751). A
// comment is no event in any protocol, so another account may still answer
// after it, its stream going on in the same response (pass).
func (h *holdWriter) keepAlive() {
	a := h.alive
	// a Gemini stream has none, as geminiEncoder.keepalive: agy fails on
	// the comment ("invalid stream chunk: : keepalive", #934)
	if a == nil || a.proto == provider.Gemini || time.Since(a.at) < keepaliveEvery {
		return
	}
	if !a.sent {
		dst := h.w.Header()
		for k, v := range h.header {
			if k != resetsHeader && k != refusedHeader && k != "Content-Length" {
				dst[k] = v
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
	}
}

// flow lets a held stream through, and what follows it.
func (h *holdWriter) flow() {
	h.pass()
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
	if !h.whole {
		return
	}
	if msg, ok := refusedReply(h.held.Bytes()); ok {
		h.failure, h.failMsg, h.refused = refusedStatus, msg, true
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

// release sends a held reply after all: nobody else is left to try.
func (h *holdWriter) release() {
	if h.passing || h.status == 0 {
		return
	}
	if h.alive != nil && h.alive.sent && !h.stream {
		// an error status, once the agent has a stream: told as its error
		h.passing = true
		streamError(h.w, h.alive.proto, h.status, provider.APIError(h.held.Bytes(), http.StatusText(h.status)))
		return
	}
	h.pass()
	h.w.Write(h.held.Bytes())
	h.flush()
}

// watchEvery is how often watch looks at a try.
var watchEvery = time.Second

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
				if time.Since(began) >= keepHeldAfter {
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
	if h.slow || h.ended || h.alive == nil || h.alive.proto == provider.Gemini || h.ctx != nil && h.ctx.Err() != nil {
		return
	}
	if !h.heard.IsZero() && time.Since(h.heard) >= keepaliveLongest {
		return // a stream stuck this long is left for a timeout to end
	}
	if !h.passing {
		// only a stream, or no reply yet to an agent that asked for one;
		// an error held is told as the stream's own once the 200 is out
		if h.hold && h.failure == 0 && !h.whole && h.status < 400 && (h.stream || h.status == 0) {
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
	eventRefusal   // the reply's end, by the vendor's safety filter
	eventBuffering // a lead saying the reply is held back for safety checks
	eventThinking  // reasoning, before anything is said: a refusal may yet end it
)

// refusedStatus is what a refusal with nothing said is answered as: a
// request the agent is told not to send again as it is, rather than a
// reply that looks whole but empty, which Codex asks for again and again,
// each time paying for the prompt (#248).
const refusedStatus = http.StatusBadRequest

// filterReasons are how a vendor says its safety filter stopped the reply:
// Anthropic's stop_reason, OpenAI's finish_reason and incomplete reason,
// an OpenAI or Azure error's code, and Gemini's finishReason and
// blockReason.
var filterReasons = map[string]string{
	"refusal":                  "refusal",
	"content_filter":           "content_filter",
	"content_policy_violation": "content_filter",
	"SAFETY":                   "safety",
	"PROHIBITED_CONTENT":       "safety",
	"BLOCKLIST":                "safety",
	"SPII":                     "safety",
	"IMAGE_SAFETY":             "safety",
}

// refusedNote is what a refusal is noted as: the vendor's own reason.
func refusedNote(reason string) string {
	return "safety filter: " + reason
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
// text, reasoning, a call. What isn't known is taken as saying something.
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
	return true
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
				return eventRefusal, refusedStatus, note
			}
		}
		if note, ok := policyRefusal(raw); ok {
			return eventRefusal, refusedStatus, note
		}
		return errOf(raw)
	case len(v.Error) > 0 && string(v.Error) != "null":
		if note, ok := policyRefusal(v.Error); ok {
			return eventRefusal, refusedStatus, note
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
			StopReason string `json:"stop_reason"`
		}
		if json.Unmarshal(v.Delta, &d) == nil && d.StopReason == "refusal" {
			return refusal(d.StopReason)
		}
	case typ == "response.incomplete" || typ == "response.completed" && v.Response.Status == "incomplete":
		if d := v.Response.IncompleteDetails; d != nil {
			if r, ok := filterReasons[d.Reason]; ok {
				return refusal(r)
			}
		}
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
				return eventContent, 0, ""
			}
		}
		if thinks {
			return eventThinking, 0, ""
		}
		return eventLead, 0, ""
	}
	return eventContent, 0, ""
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
			if len(msg) > 300 {
				msg = msg[:300] + "…"
			}
			return refusedNote(reason) + " — " + msg, true
		}
		return refusedNote(reason), true
	}
	switch r, ok := filterReasons[c]; {
	case ok:
		return note(r)
	case policyCode.MatchString(c):
		return note(c)
	case c == "invalid_prompt" && flaggedWords.MatchString(msg):
		return note(c)
	}
	return "", false
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
		StopReason string       `json:"stop_reason"`
		Content    []streamPart `json:"content"`
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
		return refusedNote(r), true
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

// streamStatus is the status an error in a stream stands for, as a vendor
// would have answered it before streaming.
func streamStatus(msg string) int {
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "overloaded"):
		return 529
	case creditWords.MatchString(msg) && !strings.Contains(l, "rate"):
		return 402
	case strings.Contains(l, "rate_limit"), strings.Contains(l, "rate limit"), strings.Contains(l, "too many"), quotaWords.MatchString(msg):
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
// doesn't list the model, or one resting.
func pinTo(want string, cands []candidate, pl planned) ([]candidate, planned, int, string) {
	match := func(w Weighed) bool { return strings.EqualFold(want, w.Who) || want == w.ID }
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
