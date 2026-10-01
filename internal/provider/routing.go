package provider

// Routing: how the gateway spreads requests over the keys or accounts a
// provider has on (see Provider.Routing). Choosing by use needs to know the
// use without waiting for it, so a subscription's allowance is read from
// what was fetched last, and fetched again behind the request when it has
// gone stale — or when an account says it has run out.

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// The routings besides the default, smart, in order.
const (
	Ordered   = "order"
	Rotate    = "rotate"
	LeastUsed = "usage"
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
	var a Allowance
	for _, w := range ws {
		if w.Aside {
			continue
		}
		l := Limit{Used: w.Used, Span: w.Span, Model: w.Model, matches: w.matches}
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
