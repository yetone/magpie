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
// The cap is the user's, not the vendor's: what it holds back never spends
// a Codex reset. The resets are spent on the vendor's own 100% (weekUsedUp,
// usedUp, BackAt, heldUp), which the cap doesn't touch.

import (
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"
)

// MinCap and MaxCap bound an account's cap, in percent: none is no cap.
const (
	MinCap = 1
	MaxCap = 99
)

// AccountCap is the share (1–99) of its windows the account user of p is
// used to at most, 0 when it has no cap. An account made afresh beside the
// agent's own (AlsoOn) carries none of the provider's settings: they are
// read from providers.json then.
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

// WindowCaps is what holds an account at a share of its windows: its usage
// cap for every window (All), the share of each window the user set one
// on (Windows, by WindowCapID; 100 is none on that window), and Credits
// for a Codex account set not to spend its credits, which holds any window
// with no cap at the vendor's own 100%.
type WindowCaps struct {
	All     int
	Windows map[string]int
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

// Holds says whether any window can hold the account before the vendor
// does.
func (c WindowCaps) Holds() bool {
	if c.All > 0 || c.Credits {
		return true
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
	caps, wins := p.AccountCaps, p.AccountWindowCaps
	if caps == nil && wins == nil {
		if s, ok := storedPicks(p.ID); ok {
			caps, wins = s.AccountCaps, s.AccountWindowCaps
		}
	}
	k := accountKey(user)
	return WindowCaps{All: caps[k], Windows: wins[k]}
}

// AccountCapsOf is CapsOf of the provider id.
func AccountCapsOf(id, user string) WindowCaps {
	if s, ok := storedPicks(id); ok {
		k := accountKey(user)
		return WindowCaps{All: s.AccountCaps[k], Windows: s.AccountWindowCaps[k]}
	}
	return WindowCaps{}
}

// SetWindowCap sets the share of the window named window of the account ref
// of the subscription id: 1–99, 100 for no cap on that window whatever the
// account's cap, or 0 to have it follow the account's cap again.
func SetWindowCap(id, ref, window string, cap int) error {
	if cap != 0 && (cap < MinCap || cap > 100) {
		return fmt.Errorf("a window's cap is a share from %d%% to %d%%, or 100%% for none on it, not %d%%", MinCap, MaxCap, cap)
	}
	w := WindowCapID(window)
	if w == "" {
		return fmt.Errorf("name the usage window to cap")
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account == nil {
		return fmt.Errorf("%s has keys, not subscription accounts with usage windows to cap", p.Name)
	}
	r, ok := p.accountRef(ref)
	if !ok {
		return fmt.Errorf("%s has no account %q", p.Name, ref)
	}
	k := accountKey(r)
	if p.AccountWindowCaps[k][w] == cap {
		return nil
	}
	all := map[string]map[string]int{}
	for u, m := range p.AccountWindowCaps {
		all[u] = maps.Clone(m)
	}
	if all[k] == nil {
		all[k] = map[string]int{}
	}
	if cap == 0 {
		delete(all[k], w)
	} else {
		all[k][w] = cap
	}
	if len(all[k]) == 0 {
		delete(all, k)
	}
	p.AccountWindowCaps = all
	return Save(*p)
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
	if p.Account == nil {
		return fmt.Errorf("%s has keys, not subscription accounts with usage windows to cap", p.Name)
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

// CapHold is how an account is held by its caps for a model: the share
// used of the fullest window holding it, that window's cap, when the last
// of those windows renews (zero when one doesn't say), and Credits when
// only the credits hold it, at 100%.
type CapHold struct {
	Used    float64
	Cap     int
	Back    time.Time
	Credits bool
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
	var h *CapHold
	unknown, byCap := false, false
	for _, l := range a {
		share, credits := caps.Of(l.Name)
		if share <= 0 || !l.applies(model) || l.Used < float64(share) {
			continue
		}
		if !l.Resets.IsZero() && !l.Resets.After(now) {
			continue // renewed since it was read
		}
		if h == nil {
			h = &CapHold{}
		}
		if l.Used > h.Used || h.Cap == 0 {
			h.Used, h.Cap = l.Used, share
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

// capReached says whether an account's windows that stop it for every
// model are, one of them, at or past its own share of caps at now —
// usedPast at the share, those renewed since they were read left out. No
// caps reach nothing.
func capReached(q SubscriptionQuota, caps WindowCaps, now time.Time) bool {
	for _, w := range q.Windows {
		share, _ := caps.Of(w.Name)
		if share <= 0 || w.Aside || w.Model != "" || w.Used < float64(share) {
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
			w.Capped = !w.Aside && !w.Unlimited
			w.CapsSome = w.Capped && (w.Model != "" || w.matches != nil)
			if w.Capped {
				w.CapID = WindowCapID(w.Name)
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
