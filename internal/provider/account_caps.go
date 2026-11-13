package provider

// An account's usage cap: the share of its usage windows a subscription
// account is used to at most (a Discord ask: a new Claude account kept to
// 70% of its five hours and its week, so it isn't run hard). Once a window
// that counts the model is at or past it, routing takes the account for
// used up — as at 100% — and goes on to the user's other accounts; the
// account is free again when that window renews. The gateway leaves it out
// of a request (gateway/fallback.go), and Codex or Claude Code signed in
// to it are signed in to another (NextLogin).
//
// One share for every window by default, and a share of its own for any
// window the user sets one on (willz on Discord: a friend's account stops
// at 50% of its five hours while its week may go to 40%). A window is known
// by its name as the vendor's reading gives it, in lower case
// (WindowCapID): "5 hours", "weekly", "7 days · opus", a pool's "gemini ·
// 5 hours" — the name routing already tells one reading's windows from the
// next by. The windows differ by vendor, so the overrides are kept per
// account (AccountWindowCaps), set where the window is shown. A window's
// own share is 1–99, or 100 for "no cap on this window" under an account
// cap; one with none of its own takes the account's. The account is held
// while any window is at or past its own share. The on-demand windows
// (Aside) are not the allowance and never count.
//
// A window whose reading counts it in a unit, with its total (WorkBuddy's
// credits, a plan's requests or tokens), can instead be capped at a fixed
// amount of that unit (9won_25273 on Discord: at most N credits a day of a
// daily-credits window, then the next account), kept per account and
// window (AccountWindowAmounts). The account is held once the window's
// count is at or past the amount, as at a share; a window read without a
// total leaves the amount aside and takes the account's share. A window
// has a share of its own or an amount, never both: setting one clears the
// other.
//
// The cap reads the vendor's own count of the window, so what the account
// spent outside magpie (the vendor's app, another machine) counts toward
// it as the vendor reports it; magpie can only hold back its own requests.
//
// A key whose own windows magpie reads has the same cap (key_allowance.go),
// set by the key's id: once a window is at or past it, routing goes on to
// the provider's other keys and its fallbacks, and the key is back when the
// window renews. A key whose windows aren't known (not read yet, or the
// read failed) is not held.
//
// The cap is the user's, not the vendor's: what it holds back never spends
// a Codex reset. The resets are spent on the vendor's own 100% (weekUsedUp,
// usedUp, BackAt, heldUp), which the cap doesn't touch.

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// TakesUsageCap says whether p has usage windows a cap can hold it at: a
// subscription's accounts, or the keys of a vendor whose keys tell their
// plan's windows (an OpenCode Go key's 5 hours, week and month, a sub2api
// key's limits: key_allowance.go). A key's cap is set and kept as an
// account's, by the key's id, and read from the key's windows as last read.
func (p Provider) TakesUsageCap() bool { return p.Account != nil || readsKeyWindows(p) }

// MinCap and MaxCap bound an account's cap, in percent: none is no cap.
const (
	MinCap = 1
	MaxCap = 99
)

// AccountCap is the share (1–99) of its windows the account user of p is
// used to at most, 0 when it has no cap. An account not had from All or
// AlsoOn (made afresh by its sign-in) carries none of the provider's
// settings: they are read from providers.json then.
func (p Provider) AccountCap(user string) int {
	caps := p.AccountCaps
	if caps == nil {
		if s, ok := storedPicks(p.ID); ok {
			caps = s.AccountCaps
		}
	}
	return caps[accountKey(user)]
}

// AccountCapOf is AccountCap of the provider id, 0 when there is none.
func AccountCapOf(id, user string) int {
	if s, ok := storedPicks(id); ok {
		return s.AccountCaps[accountKey(user)]
	}
	return 0
}

// KeyCapHeld is how the key k of p is held at the usage cap set on it for
// model at now, by the windows read gives of it (KeyAllowance, or a test's):
// nil when it has no cap, its windows aren't known (not read yet, or the
// read failed: unknown is not spent) or each is below its share.
func (p Provider) KeyCapHeld(k KeyAccount, model string, now time.Time, read func(Provider) (Allowance, bool)) *CapHold {
	caps := p.CapsOf(keyID(k.Key))
	if !caps.Holds() {
		return nil
	}
	al, ok := read(p.WithKey(k))
	if !ok {
		return nil
	}
	return al.CapHeld(model, caps, now)
}

// KeyAccountOf is the key of p with the id ref (KeyInfo.ID), and whether
// p has it.
func (p Provider) KeyAccountOf(ref string) (KeyAccount, bool) {
	i, ok := findKey(&p, ref)
	switch {
	case !ok:
		return KeyAccount{}, false
	case i < 0:
		return p.first(), true
	}
	return p.Keys[i], true
}

// WindowCaps is what holds an account at a share of its windows: its usage
// cap for every window (All), the share of each window the user set one
// on (Windows, by WindowCapID; 100 is none on that window), and Credits
// for a Codex account set not to spend its credits, which holds any window
// with no cap at the vendor's own 100%.
type WindowCaps struct {
	All     int
	Windows map[string]int
	// Amounts are the windows capped at a fixed amount of their own unit
	// instead, by WindowCapID (AccountWindowAmounts)
	Amounts map[string]float64
	Credits bool
}

// WindowCapID is how a window is known to its cap: its name, trimmed, in
// lower case.
func WindowCapID(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// Of is the share at which the window named name holds the account, 0 for
// none, and whether it is the credits that hold it there.
func (c WindowCaps) Of(name string) (share int, credits bool) {
	share = c.All
	if v := c.Windows[WindowCapID(name)]; v > 0 {
		share = v
	}
	if share >= 100 {
		share = 0
	}
	if share <= 0 && c.Credits {
		return 100, true
	}
	return share, false
}

// limitShare is the share at which a window holds the account, as Of
// (own when aside), with an amount cap on it read against the window's
// count: amount is then the cap in the window's unit, and share that
// amount as a share of the window's total of. A window read without a
// total (of 0) leaves its amount aside.
func (c WindowCaps) limitShare(name string, of float64, aside bool) (share float64, credits bool, amount float64) {
	if a := c.Amounts[WindowCapID(name)]; a > 0 && of > 0 {
		return a / of * 100, false, a
	}
	if aside {
		return float64(c.own(name)), false, 0
	}
	n, credits := c.Of(name)
	return float64(n), credits, 0
}

// past says whether a window at used percent, count of its unit, is at or
// past the cap limitShare gave: by its count when the cap is an amount.
func past(share, amount, used, count float64) bool {
	if share <= 0 {
		return false
	}
	if amount > 0 {
		return count >= amount
	}
	return used >= share
}

// own is the share at which a window set aside holds the account (Limit's
// aside): only a cap the user set on that window itself, never the
// account's or the credits', 0 for none.
func (c WindowCaps) own(name string) int {
	if v := c.Windows[WindowCapID(name)]; v >= MinCap && v <= MaxCap {
		return v
	}
	return 0
}

// Holds says whether any window can hold the account before the vendor
// does.
func (c WindowCaps) Holds() bool {
	if c.All > 0 || c.Credits {
		return true
	}
	for _, v := range c.Amounts {
		if v > 0 {
			return true
		}
	}
	for _, v := range c.Windows {
		if v >= MinCap && v <= MaxCap {
			return true
		}
	}
	return false
}

// CapsOf is the caps of the account user of p: its usage cap
// and its windows' own. As with AccountCap, an account made afresh beside
// the agent's own reads them from providers.json.
func (p Provider) CapsOf(user string) WindowCaps {
	caps, wins, amts := p.AccountCaps, p.AccountWindowCaps, p.AccountWindowAmounts
	if caps == nil && wins == nil && amts == nil {
		if s, ok := storedPicks(p.ID); ok {
			caps, wins, amts = s.AccountCaps, s.AccountWindowCaps, s.AccountWindowAmounts
		}
	}
	k := accountKey(user)
	return WindowCaps{All: caps[k], Windows: wins[k], Amounts: amts[k]}
}

// AccountCapsOf is CapsOf of the provider id.
func AccountCapsOf(id, user string) WindowCaps {
	if s, ok := storedPicks(id); ok {
		k := accountKey(user)
		return WindowCaps{All: s.AccountCaps[k], Windows: s.AccountWindowCaps[k], Amounts: s.AccountWindowAmounts[k]}
	}
	return WindowCaps{}
}

// SetWindowCap sets the share of the window named window of the account ref
// of the subscription id: 1–99, 100 for no cap on that window whatever the
// account's cap, or 0 to have it follow the account's cap again.
func SetWindowCap(id, ref, window string, cap int) error {
	return SetWindowCaps(id, ref, []string{window}, cap)
}

// SetWindowCaps is SetWindowCap of several windows at once: a family's
// models' own (Antigravity's Gemini 3.1 Pro (High) and (Low)), which the
// GUI shows as one meter.
func SetWindowCaps(id, ref string, windows []string, cap int) error {
	if cap != 0 && (cap < MinCap || cap > 100) {
		return fmt.Errorf("a window's cap is a share from %d%% to %d%%, or 100%% for none on it, not %d%%", MinCap, MaxCap, cap)
	}
	var ws []string
	for _, w := range windows {
		if w = WindowCapID(w); w == "" {
			return fmt.Errorf("name the usage window to cap")
		}
		ws = append(ws, w)
	}
	if len(ws) == 0 {
		return fmt.Errorf("name the usage window to cap")
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	if !p.TakesUsageCap() {
		return fmt.Errorf("%s has keys with no usage windows magpie reads, not subscription accounts with windows to cap", p.Name)
	}
	r, ok := p.accountRef(ref)
	if !ok {
		return fmt.Errorf("%s has no account %q", p.Name, ref)
	}
	k := accountKey(r)
	if !slices.ContainsFunc(ws, func(w string) bool { return p.AccountWindowCaps[k][w] != cap || p.AccountWindowAmounts[k][w] > 0 }) {
		return nil
	}
	p.AccountWindowAmounts = withoutAmounts(p.AccountWindowAmounts, k, ws)
	all := map[string]map[string]int{}
	for u, m := range p.AccountWindowCaps {
		all[u] = maps.Clone(m)
	}
	if all[k] == nil {
		all[k] = map[string]int{}
	}
	for _, w := range ws {
		if cap == 0 {
			delete(all[k], w)
		} else {
			all[k][w] = cap
		}
	}
	if len(all[k]) == 0 {
		delete(all, k)
	}
	p.AccountWindowCaps = all
	return Save(*p)
}

// SetWindowAmount caps the windows named windows of the account ref of the
// subscription id at amount of their own unit (credits, requests, tokens),
// in place of a share of their own; 0 lifts it, and the windows follow the
// account's cap again.
func SetWindowAmount(id, ref string, windows []string, amount float64) error {
	if amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return fmt.Errorf("a window's cap is an amount above 0, not %v", amount)
	}
	var ws []string
	for _, w := range windows {
		if w = WindowCapID(w); w == "" {
			return fmt.Errorf("name the usage window to cap")
		}
		ws = append(ws, w)
	}
	if len(ws) == 0 {
		return fmt.Errorf("name the usage window to cap")
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	if !p.TakesUsageCap() {
		return fmt.Errorf("%s has keys with no usage windows magpie reads, not subscription accounts with windows to cap", p.Name)
	}
	r, ok := p.accountRef(ref)
	if !ok {
		return fmt.Errorf("%s has no account %q", p.Name, ref)
	}
	k := accountKey(r)
	if !slices.ContainsFunc(ws, func(w string) bool {
		_, own := p.AccountWindowCaps[k][w]
		return p.AccountWindowAmounts[k][w] != amount || own
	}) {
		return nil
	}
	amts := withoutAmounts(p.AccountWindowAmounts, k, nil)
	if amount > 0 {
		if amts == nil {
			amts = map[string]map[string]float64{}
		}
		if amts[k] == nil {
			amts[k] = map[string]float64{}
		}
		for _, w := range ws {
			amts[k][w] = amount
		}
	} else {
		amts = withoutAmounts(amts, k, ws)
	}
	p.AccountWindowAmounts = amts
	// an amount stands in for the window's own share
	if own := p.AccountWindowCaps[k]; own != nil {
		all := map[string]map[string]int{}
		for u, m := range p.AccountWindowCaps {
			all[u] = maps.Clone(m)
		}
		for _, w := range ws {
			delete(all[k], w)
		}
		if len(all[k]) == 0 {
			delete(all, k)
		}
		p.AccountWindowCaps = all
	}
	return Save(*p)
}

// withoutAmounts is a copy of m with account k's amounts on windows ws
// left out (none for a nil ws); nil for none left.
func withoutAmounts(m map[string]map[string]float64, k string, ws []string) map[string]map[string]float64 {
	var out map[string]map[string]float64
	for u, as := range m {
		c := maps.Clone(as)
		if u == k {
			for _, w := range ws {
				delete(c, w)
			}
		}
		if len(c) == 0 {
			continue
		}
		if out == nil {
			out = map[string]map[string]float64{}
		}
		out[u] = c
	}
	return out
}

// SetAccountCap caps the account ref of the subscription id at cap percent
// of its windows; 0 lifts the cap.
func SetAccountCap(id, ref string, cap int) error {
	if cap != 0 && (cap < MinCap || cap > MaxCap) {
		return fmt.Errorf("a cap is a share from %d%% to %d%% of each window, not %d%%", MinCap, MaxCap, cap)
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	if !p.TakesUsageCap() {
		return fmt.Errorf("%s has keys with no usage windows magpie reads, not subscription accounts with windows to cap", p.Name)
	}
	r, ok := p.accountRef(ref)
	if !ok {
		return fmt.Errorf("%s has no account %q", p.Name, ref)
	}
	if cap == 0 && p.AccountCaps[accountKey(r)] == 0 {
		return nil
	}
	caps := map[string]int{}
	for k, v := range p.AccountCaps {
		caps[k] = v
	}
	if cap == 0 {
		delete(caps, accountKey(r))
	} else {
		caps[accountKey(r)] = cap
	}
	p.AccountCaps = caps
	return Save(*p)
}

// ParseCap reads a cap as the CLI takes it: "70", "70%", or off (none, 0,
// -) for no cap.
func ParseCap(s string) (int, error) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	switch strings.ToLower(s) {
	case "off", "none", "no", "-", "0", "100":
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < MinCap || n > MaxCap {
		return 0, fmt.Errorf("a cap is a share from %d to %d (percent), or off, not %q", MinCap, MaxCap, s)
	}
	return n, nil
}

// normalAccountCaps is m as it is kept: keys in lower case, caps within
// MinCap–MaxCap, accounts without one left out; nil for none.
func normalAccountCaps(m map[string]int) map[string]int {
	var out map[string]int
	for u, c := range m {
		u = accountKey(u)
		if u == "" || c < MinCap || c > MaxCap {
			continue
		}
		if out == nil {
			out = map[string]int{}
		}
		out[u] = c
	}
	return out
}

// normalWindowCaps is m as it is kept: accounts and windows in lower case,
// shares within MinCap–100, empty ones left out; nil for none.
func normalWindowCaps(m map[string]map[string]int) map[string]map[string]int {
	var out map[string]map[string]int
	for u, ws := range m {
		u = accountKey(u)
		for w, c := range ws {
			w = WindowCapID(w)
			if u == "" || w == "" || c < MinCap || c > 100 {
				continue
			}
			if out == nil {
				out = map[string]map[string]int{}
			}
			if out[u] == nil {
				out[u] = map[string]int{}
			}
			out[u][w] = c
		}
	}
	return out
}

// normalWindowAmounts is m as it is kept: accounts and windows in lower
// case, amounts above 0, empty ones left out; nil for none.
func normalWindowAmounts(m map[string]map[string]float64) map[string]map[string]float64 {
	var out map[string]map[string]float64
	for u, ws := range m {
		u = accountKey(u)
		for w, a := range ws {
			w = WindowCapID(w)
			if u == "" || w == "" || !(a > 0) || math.IsInf(a, 0) {
				continue
			}
			if out == nil {
				out = map[string]map[string]float64{}
			}
			if out[u] == nil {
				out[u] = map[string]float64{}
			}
			out[u][w] = a
		}
	}
	return out
}

// CapHold is how an account is held by its caps for a model: the share
// used of the fullest window holding it, that window's cap, when the last
// of those windows renews (zero when one doesn't say), and Credits when
// only the credits hold it, at 100%.
type CapHold struct {
	Used    float64
	Cap     int
	Back    time.Time
	Credits bool
	// Amount is the cap when the fullest window holding it is capped at a
	// fixed amount (AccountWindowAmounts): Count of it used, in Unit. Cap
	// is then that amount as a share, rounded up.
	Amount, Count float64
	Unit          string
}

// CapHeld says how an allowance, as last known, holds the account for
// model at now: some window counting model at or past its own share of
// caps (WindowCaps.Of), not renewed since; nil when none is. A window
// whose reset has passed is empty again; no caps hold nothing.
func (a Allowance) CapHeld(model string, caps WindowCaps, now time.Time) *CapHold {
	if !caps.Holds() {
		return nil
	}
	model = strings.ToLower(model)
	caps = a.extraCapped(model, caps, now)
	var h *CapHold
	unknown, byCap := false, false
	for _, l := range a.capLimits(model) {
		if l.extra {
			continue // held by extraCapped's 100%, not by itself
		}
		share, credits, amount := caps.limitShare(l.Name, l.Of, l.aside)
		if !past(share, amount, l.Used, l.Amount) {
			continue
		}
		if !l.Resets.IsZero() && !l.Resets.After(now) {
			continue // renewed since it was read
		}
		if h == nil {
			h = &CapHold{}
		}
		if l.Used > h.Used || h.Cap == 0 {
			h.Used, h.Cap = l.Used, min(int(math.Ceil(share)), 100)
			h.Amount, h.Count, h.Unit = 0, 0, ""
			if amount > 0 {
				h.Amount, h.Count, h.Unit = amount, l.Amount, l.Unit
			}
		}
		byCap = byCap || !credits
		if l.Resets.IsZero() {
			unknown = true
		} else if l.Resets.After(h.Back) {
			h.Back = l.Resets
		}
	}
	if h == nil {
		return nil
	}
	if unknown {
		h.Back = time.Time{}
	}
	h.Credits = !byCap
	return h
}

// extraCapped is caps with Credits set — the account held at 100% of any
// plan window with no cap of its own, as ClaudeNoExtraUsage holds it —
// when a counts model with a Claude account's extra usage (Limit.extra)
// at or past a cap the user set on it, not renewed since: what the plan
// has left is still used, and no more extra usage is spent.
func (a Allowance) extraCapped(model string, caps WindowCaps, now time.Time) WindowCaps {
	if caps.Credits {
		return caps
	}
	for _, l := range a.capLimits(model) {
		if !l.extra {
			continue
		}
		share, _, amount := caps.limitShare(l.Name, l.Of, l.aside)
		if !past(share, amount, l.Used, l.Amount) || !l.Resets.IsZero() && !l.Resets.After(now) {
			continue
		}
		caps.Credits = true
		return caps
	}
	return caps
}

// NearCap says whether an allowance, as last known, has some window
// counting model within points of its own share of caps (or past it) at
// now, not renewed since: a careful provider reads such an account again
// before it is sent a request (Provider.CapCareful). No caps are near
// nothing.
func (a Allowance) NearCap(model string, caps WindowCaps, points float64, now time.Time) bool {
	if !caps.Holds() {
		return false
	}
	model = strings.ToLower(model)
	caps = a.extraCapped(model, caps, now)
	for _, l := range a.capLimits(model) {
		share, _, amount := caps.limitShare(l.Name, l.Of, l.aside)
		if share <= 0 || l.Used < share-points && (amount <= 0 || l.Amount < amount) {
			continue
		}
		if !l.Resets.IsZero() && !l.Resets.After(now) {
			continue // renewed since it was read
		}
		return true
	}
	return false
}

// capReached says whether an account's windows that stop it for every
// model are, one of them, at or past its own share of caps at now —
// usedPast at the share, those renewed since they were read left out. No
// caps reach nothing.
func capReached(q SubscriptionQuota, caps WindowCaps, now time.Time) bool {
	for _, w := range q.Windows {
		share, _, amount := caps.limitShare(w.Name, w.Limit, false)
		if w.Aside || w.Model != "" || !past(share, amount, w.Used, w.Amount) {
			continue
		}
		if w.ResetsAt != nil && !w.ResetsAt.After(now) {
			continue
		}
		return true
	}
	return false
}

// WithCapped is m with Capped set on each window an account's usage cap
// counts, for the GUI to mark the cap and say an account is held at it as
// routing does — a window one model's own counts for that model, so it is
// marked as the rest are; the windows are copied, the cache's left as they
// are.
func WithCapped(m map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	out := make(map[string]SubscriptionQuota, len(m))
	for u, q := range m {
		ws := make([]QuotaWindow, len(q.Windows))
		for i, w := range q.Windows {
			// a pool's own window holds its models (allowanceOf)
			pool := w.PoolWindow() && len(PoolMembers(q.Windows, w)) > 0
			w.Capped = (!w.Aside || pool) && !w.Unlimited
			w.CapsSome = w.Capped && (w.Model != "" || w.matches != nil || pool)
			// set aside, it still holds at a cap the user set on it
			// before (allowanceOf), and at no other
			w.OwnCap = w.Aside && !w.Unlimited && !w.Text && w.Model == "" && !pool
			w.Capped = w.Capped || w.OwnCap
			if w.Capped {
				w.CapID = WindowCapID(w.Name)
			}
			if pool {
				w.CapID = WindowCapID(w.LineName())
			}
			ws[i] = w
		}
		if q.Windows != nil {
			q.Windows = ws
		}
		out[u] = q
	}
	return out
}
