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
	// Weighted spreads a provider's requests over its keys by each key's
	// weight (KeyAccount.Weight, #841), smoothly: 3 and 1 go a, a, b, a…
	Weighted = "weight"
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

// SetSink sends a provider's keys or accounts rate limited with quota
// left to the back of its order, or lets them keep their place
// (Provider.Sink).
func SetSink(id string, sink bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.Sink = sink
	return Save(*p)
}

// SetKeepLogin keeps Codex or Claude Code signed in to the account the
// user made first (Provider.KeepLogin), or lets magpie move it on again.
// Either way it is no longer kept on one of the user's choosing
// (KeepLoginAs): it is signed in to the first again.
func SetKeepLogin(id string, keep bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account == nil || p.Account.Agent != p.ID || !slices.Contains(switchedAgents, p.ID) {
		return fmt.Errorf("magpie doesn't sign %s in to another of its accounts", p.Name)
	}
	chosen := p.KeepLogin && p.KeepLoginAs != ""
	p.KeepLogin, p.KeepLoginAs = keep, ""
	if err := Save(*p); err != nil {
		return err
	}
	if chosen {
		return signInToFirst(p.ID)
	}
	return nil
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
	// renewed: when an account's windows were last started again (a
	// Codex reset spent), by agent/user: a reading begun before says
	// nothing of them
	renewed map[string]time.Time
}

// firstWait is how long a request waits for an agent's allowances the
// first time they are asked for, so the first requests after magpie
// starts are routed by them too; later ones never wait.
var firstWait = 3 * time.Second

// Allowance is an account's allowance windows as last known.
type Allowance []Limit

// Limit is one window of an allowance.
type Limit struct {
	Used   float64       // share used, 0–100
	Resets time.Time     // zero when not known
	Span   time.Duration // how long the window runs; zero when not known
	Model  string        // the only models it counts, by a word in their ids
	// Amount of Of in Unit: the window's own count, used, when the vendor
	// counts it so (QuotaWindow.Amount)
	Amount, Of float64
	Unit       string
	matches    func(string) bool
	partial    bool // of a reading that may leave windows out (QuotaWindow.partial)
	// ResetRunsOut is when the reset the account spends by itself before
	// it runs out does (resetRunsOut): spent then, it starts this window
	// again — at Restarts, which routing takes for the window's renewal
	// when it is the sooner. Zero when none will be spent so.
	ResetRunsOut time.Time
}

// Restarts is when an auto-used reset starts the account's windows again,
// as the Codex reset about to run out is spent (expiringResetSpent):
// resetExpiryLead before it runs out, or now when the account is held up
// past then. What they have left then is lost, as at their own reset, so
// Weekly pace and Smart take it for their renewal when it comes sooner
// (#717, #718). Zero when none will be.
func (a Allowance) Restarts(now time.Time) time.Time {
	for _, l := range a {
		if !l.ResetRunsOut.IsZero() {
			stopped, back := a.heldUp()
			return expiringResetSpent(l.ResetRunsOut, now, stopped, back)
		}
	}
	return time.Time{}
}

// heldUp is usedUp and BackAt by an allowance: whether a window that
// stops the account, for every model, is used up, and when the last of
// those starts again (zero when one doesn't say).
func (a Allowance) heldUp() (stopped bool, back time.Time) {
	for _, l := range a {
		if l.Model != "" || l.matches != nil || l.Used < 100 {
			continue
		}
		if l.Resets.IsZero() {
			return true, time.Time{}
		}
		if !stopped || l.Resets.After(back) {
			back = l.Resets
		}
		stopped = true
	}
	return stopped, back
}

// restarted is when a window that renews at renews (zero: not known) is
// started again: at restart (Restarts) when that is the sooner.
func restarted(renews, restart time.Time) time.Time {
	if !restart.IsZero() && (renews.IsZero() || restart.Before(renews)) {
		return restart
	}
	return renews
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

// Count is the count of the window For's share is of — the fullest that
// counts model — when the vendor counts it (WorkBuddy's credits): how much
// of of is used, in unit; of is zero when it isn't counted so. A window
// whose reset has passed is empty again.
func (a Allowance) Count(model string, now time.Time) (amount, of float64, unit string) {
	model = strings.ToLower(model)
	used := -1.0
	for _, l := range a {
		if !l.applies(model) {
			continue
		}
		if !l.Resets.IsZero() && !l.Resets.After(now) {
			l.Used, l.Amount = 0, 0
		}
		if l.Used > used {
			used, amount, of, unit = l.Used, l.Amount, l.Of, l.Unit
		}
	}
	return amount, of, unit
}

// Renewal is For's renews as routing ranks them: a window not started,
// or whose reset has passed, that says how long it runs is taken to
// renew that long from now — a five hours not started renews within five
// hours, the soonest of an account that has nothing longer (#576).
func (a Allowance) Renewal(model string, now time.Time) []time.Time {
	model = strings.ToLower(model)
	restart := a.Restarts(now)
	var ls []Limit
	for _, l := range a {
		if !l.applies(model) {
			continue
		}
		if !l.Resets.After(now) {
			l.Resets = time.Time{}
			if l.Span > 0 {
				l.Resets = now.Add(l.Span)
			}
		}
		l.Resets = restarted(l.Resets, restart)
		ls = append(ls, l)
	}
	sort.SliceStable(ls, func(i, j int) bool {
		if ls[i].Span != ls[j].Span {
			return ls[i].Span > ls[j].Span
		}
		return ls[i].Resets.After(ls[j].Resets)
	})
	out := make([]time.Time, len(ls))
	for i, l := range ls {
		out[i] = l.Resets
	}
	return out
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

// Pooled says whether a refusal of model for its allowance leaves the
// account's other models alone: the windows that count model and are full
// at share are each of some models only (Opus's own week, Cursor's Other
// Models pool) — or, none known full, no window of the whole account
// counts it, only pools. Then it is that model which is out, not the
// account: Cursor's Other Models used up leaves Auto and Composer in
// theirs (Xiaopodev on X).
func (a Allowance) Pooled(model string, share float64, now time.Time) bool {
	model = strings.ToLower(model)
	full, pooled, whole := false, false, false
	for _, l := range a {
		if !l.applies(model) {
			continue
		}
		scoped := l.Model != "" || l.matches != nil
		if l.Used >= share && (l.Resets.IsZero() || l.Resets.After(now)) {
			if !scoped {
				return false
			}
			full = true
		}
		pooled = pooled || scoped
		whole = whole || !scoped
	}
	return full || pooled && !whole
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
// the rest. An account whose vendor tells no budget window, only a short
// one (Claude Enterprise's five hours), has nothing longer to keep for:
// what its five hours have left is lost at their reset, so it goes by
// that, per hour until then — the whole span when not started (#576):
// ahead of nearly every week, behind only one with more of it to lose
// sooner. Where the reading may leave a week out (a Claude account only
// heard of as Claude Code answered, partial) it is instead taken to have
// a week not started with the share its fullest window has used: those
// go by what they have used, among the rest as the fresh are. An
// account whose windows an auto-used reset starts again sooner than they
// renew (Restarts) goes by that instead, and due is then that time: what
// it has left is lost there.
func (a Allowance) Pace(model string, now time.Time) (pace float64, due time.Time) {
	model = strings.ToLower(model)
	restart := a.Restarts(now)
	// until is how long a window runs from now and renews when; a restart
	// sooner cuts it short
	cut := func(until time.Duration, renews time.Time) (time.Duration, time.Time) {
		if !restart.IsZero() && restart.Before(now.Add(until)) {
			return restart.Sub(now), restart
		}
		return until, renews
	}
	any, used := false, 0.0
	short, partial, shortPace, shortDue := false, false, 0.0, time.Time{}
	for _, l := range a {
		if !l.applies(model) {
			continue
		}
		partial = partial || l.partial
		u := min(100, max(0, l.Used))
		if !l.Resets.IsZero() && !l.Resets.After(now) {
			u = 0 // its reset has passed: empty again
		}
		if l.Span != 0 && l.Span < budgetSpan {
			used = max(used, u)
			until, renews := l.Span, time.Time{}
			if l.Resets.After(now) {
				until, renews = l.Resets.Sub(now), l.Resets
			}
			until, renews = cut(until, renews)
			if p := (100 - u) / max(until, time.Hour).Hours(); !short || p < shortPace {
				shortPace, shortDue, short = p, renews, true
			}
			continue
		}
		until, renews := l.Span, time.Time{}
		if until == 0 {
			until = weekSpan
		}
		if l.Resets.After(now) {
			until, renews = l.Resets.Sub(now), l.Resets
		}
		until, renews = cut(until, renews)
		if p := (100 - u) / max(until, time.Hour).Hours(); !any || p < pace {
			pace, due, any = p, renews, true
		}
	}
	if !any {
		if short && !partial {
			return shortPace, shortDue
		}
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
			began := time.Now()
			all := map[string]Allowance{}
			for user, q := range LoginUsage(ctx, agent) {
				if q.Error != "" || len(q.Windows) == 0 {
					continue
				}
				all[user] = allowanceOf(q.Windows, time.Now()).restartedBy(resetRunsOut(agent, user, q.Windows, q.Resets))
			}
			c.Lock()
			at := time.Now()
			for user := range all {
				if c.renewed[agent+"/"+strings.ToLower(user)].After(began) {
					// started again while this was read: not known till
					// it is read again, at once
					delete(all, user)
					at = time.Time{}
				}
			}
			c.m[agent], c.at[agent] = all, at
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
// (a Codex reset spent), so what sat out waiting for them can come back;
// and, as agent "" and the key's KeyAllowanceID, when a reading of a key's
// own windows finds one it was full in full no more: its limit raised in
// its panel, or its usage reset.
func OnRenewed(f func(agent, user string)) {
	renewedHooks.Lock()
	renewedHooks.fs = append(renewedHooks.fs, f)
	renewedHooks.Unlock()
}

var renewedHooks struct {
	sync.Mutex
	fs []func(agent, user string)
}

// renewedNow tells those OnRenewed asked, and forgets the account's
// allowance as last read: its windows are not known till read again, and
// what holds an account at a share of them (a cap, credits not spent)
// holds it no more.
func renewedNow(agent, user string) {
	forgetAllowance(agent, user)
	tellRenewed(agent, user)
}

// tellRenewed tells those OnRenewed asked.
func tellRenewed(agent, user string) {
	renewedHooks.Lock()
	fs := renewedHooks.fs
	renewedHooks.Unlock()
	for _, f := range fs {
		f(agent, user)
	}
}

// forgetAllowance leaves agent's account user out of the allowances last
// read, and has the next Allowances read them again.
func forgetAllowance(agent, user string) {
	c := &usedCache
	c.Lock()
	defer c.Unlock()
	if c.renewed == nil {
		c.renewed = map[string]time.Time{}
	}
	c.renewed[agent+"/"+strings.ToLower(user)] = time.Now()
	if c.at != nil {
		c.at[agent] = time.Time{}
	}
	m, ok := c.m[agent]
	if !ok {
		return
	}
	// a new map: the one handed out is read without the lock
	kept := make(map[string]Allowance, len(m))
	for u, a := range m {
		if !strings.EqualFold(u, user) {
			kept[u] = a
		}
	}
	c.m[agent] = kept
}

// StaleAllowance makes the next Allowances ask the vendor again for user's
// allowance rather than trust what it last said: the account just
// answered that it has run out.
func StaleAllowance(agent, user string) {
	key := agent + "/" + strings.ToLower(user)
	loginUsageCache.Lock()
	delete(loginUsageCache.m, key)
	delete(loginUsageCache.pending, key) // nor a reading asked for before
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

// restartedBy marks every window as started again by a reset spent
// before it runs out at runsOut (Limit.ResetRunsOut); zero leaves a as it
// is.
func (a Allowance) restartedBy(runsOut time.Time) Allowance {
	for i := range a {
		a[i].ResetRunsOut = runsOut
	}
	return a
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
		l := Limit{Used: w.Used, Span: w.Span, Model: w.Model, Amount: w.Amount, Of: w.Limit, Unit: w.Unit, matches: w.matches, partial: w.partial}
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
