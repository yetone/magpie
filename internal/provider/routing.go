package provider

// Routing: how the gateway spreads requests over the keys or accounts a
// provider has on (see Provider.Routing). Choosing by use needs to know the
// use without waiting for it, so a subscription's allowance is read from
// what was fetched last, and fetched again behind the request when it has
// gone stale — or when an account says it has run out.

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// The routings besides the default, smart, in order.
const (
	Ordered   = "order"
	Rotate    = "rotate"
	LeastUsed = "usage"
	Pace      = "pace"
)

// SetRouting changes how a provider's requests spread over its keys or
// accounts.
func SetRouting(id, routing string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.Routing = routing
	return Save(*p)
}

// SetKeepLogin keeps Codex or Claude Code signed in to the account the
// user made first (Provider.KeepLogin), or lets magpie move it on again.
func SetKeepLogin(id string, keep bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account == nil || p.Account.Agent != p.ID || !slices.Contains(switchedAgents, p.ID) {
		return fmt.Errorf("magpie doesn't sign %s in to another of its accounts", p.Name)
	}
	p.KeepLogin = keep
	return Save(*p)
}

// SetAffinity changes how long a provider's conversations stay with the key
// or account that answered them.
func SetAffinity(id, affinity string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.Affinity = affinity
	return Save(*p)
}

var usedCache struct {
	sync.Mutex
	m       map[string]map[string]Allowance // agent → user → allowance
	at      map[string]time.Time
	loading map[string]chan struct{} // closed when the fetch in flight is done
}

// firstWait is how long a request waits for an agent's allowances the
// first time they are asked for, so the first requests after magpie
// starts are routed by them too; later ones never wait.
var firstWait = 3 * time.Second

// Allowance is an account's allowance windows as last known.
type Allowance []Limit

// Limit is one window of an allowance.
type Limit struct {
	Used    float64       // share used, 0–100
	Resets  time.Time     // zero when not known
	Span    time.Duration // how long the window runs; zero when not known
	Model   string        // the only models it counts, by a word in their ids
	matches func(string) bool
}

func (l Limit) applies(model string) bool {
	return (l.Model == "" || strings.Contains(model, l.Model)) && (l.matches == nil || l.matches(model))
}

// For is what an allowance leaves a request for model at now: the share
// used of the fullest window that counts it, and when those windows renew,
// the biggest first — the longest, the week before the five hours in it.
// A window whose reset has passed is empty again, its next reset not known.
func (a Allowance) For(model string, now time.Time) (used float64, renews []time.Time) {
	model = strings.ToLower(model)
	var ls []Limit
	for _, l := range a {
		if !l.applies(model) {
			continue
		}
		if !l.Resets.IsZero() && !l.Resets.After(now) {
			l.Used, l.Resets = 0, time.Time{}
		}
		used = max(used, l.Used)
		ls = append(ls, l)
	}
	sort.SliceStable(ls, func(i, j int) bool {
		if ls[i].Span != ls[j].Span {
			return ls[i].Span > ls[j].Span
		}
		return ls[i].Resets.After(ls[j].Resets)
	})
	for _, l := range ls {
		renews = append(renews, l.Resets)
	}
	return used, renews
}

// Full is when an account used up for model can take it again: the last
// reset of its windows that are full, zero when none is or it isn't known.
func (a Allowance) Full(model string, share float64, now time.Time) time.Time {
	model = strings.ToLower(model)
	var t time.Time
	for _, l := range a {
		if l.applies(model) && l.Used >= share && l.Resets.After(now) && l.Resets.After(t) {
			t = l.Resets
		}
	}
	return t
}

// budgetSpan is the shortest window that is a budget rather than a rate
// cap: the week (Kiro's month too), not the five hours in it — what the
// five hours leave at their reset is nothing lost.
const budgetSpan = 24 * time.Hour

// weekSpan is the budget window an account is taken to have when its
// vendor tells none a day or longer, or tells one without saying how long
// it runs or when it renews.
const weekSpan = 7 * 24 * time.Hour

// FreshPace is the pace of an account with a whole week ahead of it:
// what one not known counts as.
const FreshPace = 100 / (7 * 24.0)

// Pace is the share of its week an account has left for model, per hour
// until that week renews: the rate it would have to be used at to spend
// the week just in time, so the account with the highest has the most to
// lose at its reset. Of several budget windows that count the model
// (Opus's own beside the general), the tightest; due is when that one
// renews, zero when it isn't known. A window not started, or whose reset
// isn't known, is taken to run its whole span from now; one that doesn't
// say how long it runs (a plugin may not) is a budget, for as long as its
// reset says, else a week. The hours until a reset are never taken as
// fewer than one, or a window a minute from renewing would outweigh all
// the rest. An account whose vendor tells no budget window (Claude's own
// usage command gives the five hours alone) is taken to have a week not
// started with the share its fullest window has used: those go by what
// they have used, among the rest as the fresh are.
func (a Allowance) Pace(model string, now time.Time) (pace float64, due time.Time) {
	model = strings.ToLower(model)
	any, used := false, 0.0
	for _, l := range a {
		if !l.applies(model) {
			continue
		}
		u := min(100, max(0, l.Used))
		if !l.Resets.IsZero() && !l.Resets.After(now) {
			u = 0 // its reset has passed: empty again
		}
		if l.Span != 0 && l.Span < budgetSpan {
			used = max(used, u)
			continue
		}
		until, renews := l.Span, time.Time{}
		if until == 0 {
			until = weekSpan
		}
		if l.Resets.After(now) {
			until, renews = l.Resets.Sub(now), l.Resets
		}
		if p := (100 - u) / max(until, time.Hour).Hours(); !any || p < pace {
			pace, due, any = p, renews, true
		}
	}
	if !any {
		return (100 - used) / weekSpan.Hours(), time.Time{}
	}
	return pace, due
}

// Allowances is each of an agent's accounts' allowance by user, as last
// known, asked for again in the background when that was over a minute
// ago. Only the very first ask waits, and not for long. An account
// missing is one not known yet.
func Allowances(agent string) map[string]Allowance {
	c := &usedCache
	c.Lock()
	if c.m == nil {
		c.m, c.at, c.loading = map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}
	}
	done := c.loading[agent]
	if done == nil && time.Since(c.at[agent]) > time.Minute {
		done = make(chan struct{})
		c.loading[agent] = done
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			all := map[string]Allowance{}
			for user, q := range LoginUsage(ctx, agent) {
				if q.Error != "" || len(q.Windows) == 0 {
					continue
				}
				all[user] = allowanceOf(q.Windows, time.Now())
			}
			c.Lock()
			c.m[agent], c.at[agent] = all, time.Now()
			delete(c.loading, agent)
			c.Unlock()
			close(done)
		}()
	}
	_, known := c.m[agent]
	c.Unlock()
	if !known && done != nil {
		select {
		case <-done:
		case <-time.After(firstWait):
		}
	}
	c.Lock()
	m, known := c.m[agent]
	c.Unlock()
	if !known {
		// the first reading is still out: what each account said last
		return lastAllowances(agent)
	}
	return m
}

// OnRenewed has f told when an account's usage windows were started again
// (a Codex reset spent), so what sat out waiting for them can come back.
func OnRenewed(f func(agent, user string)) {
	renewedHooks.Lock()
	renewedHooks.fs = append(renewedHooks.fs, f)
	renewedHooks.Unlock()
}

var renewedHooks struct {
	sync.Mutex
	fs []func(agent, user string)
}

// renewedNow tells those OnRenewed asked.
func renewedNow(agent, user string) {
	renewedHooks.Lock()
	fs := renewedHooks.fs
	renewedHooks.Unlock()
	for _, f := range fs {
		f(agent, user)
	}
}

// StaleAllowance makes the next Allowances ask the vendor again for user's
// allowance rather than trust what it last said: the account just
// answered that it has run out.
func StaleAllowance(agent, user string) {
	loginUsageCache.Lock()
	delete(loginUsageCache.m, agent+"/"+strings.ToLower(user))
	loginUsageCache.Unlock()
	// the built-in keeps Grok's usage by home; a Grok moved to its plugin
	// keeps it as "plugin:grok"'s, the line above
	if agent == "grok" {
		gs := grokLogins()
		grokHomeUsage.Lock()
		for _, g := range gs {
			if strings.EqualFold(g.User, user) {
				delete(grokHomeUsage.m, g.Home)
			}
		}
		grokHomeUsage.Unlock()
	}
	usedCache.Lock()
	if usedCache.at != nil {
		usedCache.at[agent] = time.Time{}
	}
	usedCache.Unlock()
}

// allowanceOf keeps the windows that can stop an account.
func allowanceOf(ws []QuotaWindow, now time.Time) Allowance {
	// Antigravity keeps the vendor's ids in its windows (and on disk),
	// while requests can name the collapsed model. Use the same families
	// as the picker, without needing the live catalog to have been saved.
	var raw []catalog.Model
	for _, w := range ws {
		if w.Family != "" && w.Model != "" && !w.Aside {
			raw = append(raw, catalog.Model{ID: w.Model})
		}
	}
	families := map[string]map[string]bool{}
	for _, f := range antigravityFamilies(raw) {
		if len(f.variants) > 1 {
			ids := map[string]bool{f.id: true}
			for _, v := range f.variants {
				ids[v.ID] = true
			}
			for _, v := range f.variants {
				families[v.ID] = ids
			}
		}
	}
	var a Allowance
	for _, w := range ws {
		if w.Aside {
			continue
		}
		l := Limit{Used: w.Used, Span: w.Span, Model: w.Model, matches: w.matches}
		if ids := families[w.Model]; ids != nil && w.Family != "" && w.matches == nil {
			l.Model = ""
			l.matches = func(model string) bool { return ids[model] }
		}
		switch {
		case w.ResetsAt != nil:
			l.Resets = *w.ResetsAt
		case w.ResetSecs > 0:
			l.Resets = now.Add(time.Duration(w.ResetSecs) * time.Second)
		}
		a = append(a, l)
	}
	return a
}
