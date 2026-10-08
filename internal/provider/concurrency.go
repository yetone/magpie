package provider

import (
	"fmt"
	"strconv"
	"strings"
)

// Concurrency is how many requests the gateway lets be out at the vendor
// at once on each of the provider's keys or accounts, 0 for no limit: the
// user's MaxConcurrency when they set one, else what the plugin giving the
// provider says it takes (its auth hook's maxConcurrency, or its
// package.json's magpie.maxConcurrency), else none.
func (p Provider) Concurrency() int {
	if p.MaxConcurrency != nil {
		return max(*p.MaxConcurrency, 0)
	}
	return p.PluginConcurrency()
}

// PluginConcurrency is what the plugin giving the provider says it takes
// at once, as it lists it now; 0 for none, or for a provider no plugin
// gives.
func (p Provider) PluginConcurrency() int {
	if !p.IsPlugin() {
		return 0
	}
	if cur, ok := PluginOf(p.ID); ok {
		return max(cur.MaxConcurrency, 0)
	}
	return max(p.Account.plugin.MaxConcurrency, 0)
}

// One account's or key's own limit (#892: a Claude account kept to two
// at once while the provider's others take more), the queue's bound and
// how long a request waits in it. The limit is the lane's — the account
// or key itself — so whatever asks for it, a model, a routing group or an
// agent, counts against the one number (gateway/concurrency.go).

// MaxLimit bounds a limit on requests at once; MaxQueueLimit how many
// may wait, MaxQueueWait how long one waits, in seconds.
const (
	MaxLimit      = 1000
	MaxQueueLimit = 10000
	MaxQueueWait  = 3600
)

// LaneLimit is how many requests may be out at once on p's key or
// account, 0 for no limit: its own (AccountConcurrency), else the
// provider's (Concurrency). An account made afresh beside the agent's own
// (AlsoOn) carries none of the provider's settings: they are read from
// providers.json then.
func (p Provider) LaneLimit() int {
	ref := ""
	switch {
	case p.Account != nil:
		ref = p.Account.User
	case p.Key != "":
		ref = KeyID(p.Key)
	}
	if ref == "" {
		return p.Concurrency()
	}
	m := p.AccountConcurrency
	if m == nil && p.Account != nil {
		if s, ok := storedPicks(p.ID); ok {
			m = s.AccountConcurrency
		}
	}
	if n, ok := m[accountKey(ref)]; ok {
		return max(n, 0)
	}
	return p.Concurrency()
}

// AccountConcurrencyOf is the account's or key's own limit, and whether it
// has one (else it takes the provider's).
func (p Provider) AccountConcurrencyOf(ref string) (int, bool) {
	n, ok := p.AccountConcurrency[accountKey(ref)]
	return n, ok
}

// AccountRefOf is the account or key of p that ref names, as its limit is
// kept: an account's name, a key's KeyID (by its id or name).
func (p Provider) AccountRefOf(ref string) (string, bool) {
	return p.accountRef(ref)
}

// SetAccountConcurrency gives the account or key ref of the provider id
// its own limit on requests at once, 0 for none; nil gives it the
// provider's again.
func SetAccountConcurrency(id, ref string, limit *int) error {
	if limit != nil && (*limit < 0 || *limit > MaxLimit) {
		return fmt.Errorf("a limit on requests at once is from 0 (none) to %d, not %d", MaxLimit, *limit)
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	r, ok := p.accountRef(ref)
	if !ok {
		if p.Account == nil {
			return fmt.Errorf("%s has no key %q", p.Name, ref)
		}
		return fmt.Errorf("%s has no account %q", p.Name, ref)
	}
	m := map[string]int{}
	for k, v := range p.AccountConcurrency {
		m[k] = v
	}
	if limit == nil {
		if _, had := m[accountKey(r)]; !had {
			return nil
		}
		delete(m, accountKey(r))
	} else {
		m[accountKey(r)] = *limit
	}
	p.AccountConcurrency = m
	return Save(*p)
}

// CheckQueue says what is wrong with a queue's bound or wait, nil when
// nothing is.
func CheckQueue(limit, wait int) error {
	if limit < 0 || limit > MaxQueueLimit {
		return fmt.Errorf("a queue holds from 0 (no bound) to %d requests, not %d", MaxQueueLimit, limit)
	}
	if wait < 0 || wait > MaxQueueWait {
		return fmt.Errorf("a request waits from 0 (as long as it takes) to %d seconds, not %d", MaxQueueWait, wait)
	}
	return nil
}

// SetQueue sets how many requests may wait for each of the provider's keys
// or accounts, and how long one waits, in seconds; 0 is no bound.
func SetQueue(id string, limit, wait int) error {
	if err := CheckQueue(limit, wait); err != nil {
		return err
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.QueueLimit, p.QueueWait = limit, wait
	return Save(*p)
}

// ParseLimit reads a limit as the CLI takes it: a whole number, 0 or off
// for no limit, default for the provider's (nil).
func ParseLimit(s string) (*int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "default", "provider", "inherit":
		return nil, nil
	case "off", "none", "no", "-", "unlimited":
		return new(int), nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > MaxLimit {
		return nil, fmt.Errorf("a limit is a whole number from 0 (none) to %d, off, or default, not %q", MaxLimit, s)
	}
	return &n, nil
}

// normalAccountConcurrency is m as it is kept: keys in lower case, limits
// within 0–MaxLimit; nil for none.
func normalAccountConcurrency(m map[string]int) map[string]int {
	var out map[string]int
	for u, n := range m {
		u = accountKey(u)
		if u == "" {
			continue
		}
		if out == nil {
			out = map[string]int{}
		}
		out[u] = min(max(n, 0), MaxLimit)
	}
	return out
}

// MaxRPMLimit bounds a limit on requests a minute.
const MaxRPMLimit = 10000

// RPMLimit is how many requests a minute each of p's keys or accounts may
// send the vendor, 0 for no limit: the provider's MaxRPM. An account made
// afresh beside the agent's own (AlsoOn) carries none of the provider's
// settings: it is read from providers.json then.
func (p Provider) RPMLimit() int {
	n := p.MaxRPM
	if n == 0 && p.Account != nil {
		if s, ok := storedPicks(p.ID); ok {
			n = s.MaxRPM
		}
	}
	return min(max(n, 0), MaxRPMLimit)
}

// CheckRPM says what is wrong with a limit on requests a minute, nil when
// nothing is.
func CheckRPM(n int) error {
	if n < 0 || n > MaxRPMLimit {
		return fmt.Errorf("a limit on requests a minute is from 0 (none) to %d, not %d", MaxRPMLimit, n)
	}
	return nil
}

// SetRPM sets how many requests a minute each of the provider's keys or
// accounts may send, 0 for no limit.
func SetRPM(id string, n int) error {
	if err := CheckRPM(n); err != nil {
		return err
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.MaxRPM = n
	return Save(*p)
}
