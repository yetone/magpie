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
// One share for every window: the windows differ by vendor (five hours and
// a week, a month, Opus's own, a plugin's per-model ones) and by name, so a
// cap per window would need each vendor's windows named in the settings,
// where the ask, and the risk it is about, is one share of the whole
// allowance. The on-demand windows (Aside) are not the allowance and never
// count.
//
// The cap is the user's, not the vendor's: what it holds back never spends
// a Codex reset. The resets are spent on the vendor's own 100% (weekUsedUp,
// usedUp, BackAt, heldUp), which the cap doesn't touch.

import (
	"fmt"
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

// CapHeld says whether an allowance, as last known, has a window counting
// model at or past cap percent at now: the account is held by its cap.
// used is the share of the fullest such window, back when the last of them
// renews (zero when one doesn't say). A window whose reset has passed is
// empty again; no cap (0) holds nothing.
func (a Allowance) CapHeld(model string, cap int, now time.Time) (held bool, used float64, back time.Time) {
	if cap <= 0 {
		return false, 0, time.Time{}
	}
	model = strings.ToLower(model)
	unknown := false
	for _, l := range a {
		if !l.applies(model) || l.Used < float64(cap) {
			continue
		}
		if !l.Resets.IsZero() && !l.Resets.After(now) {
			continue // renewed since it was read
		}
		held, used = true, max(used, l.Used)
		if l.Resets.IsZero() {
			unknown = true
		} else if l.Resets.After(back) {
			back = l.Resets
		}
	}
	if unknown {
		back = time.Time{}
	}
	return held, used, back
}

// capReached says whether an account's windows that stop it for every
// model are, one of them, at or past cap percent at now — usedPast at the
// cap, those renewed since they were read left out. No cap reaches nothing.
func capReached(q SubscriptionQuota, cap int, now time.Time) bool {
	if cap <= 0 {
		return false
	}
	for _, w := range q.Windows {
		if w.Aside || w.Model != "" || w.Used < float64(cap) {
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
			ws[i] = w
		}
		if q.Windows != nil {
			q.Windows = ws
		}
		out[u] = q
	}
	return out
}
