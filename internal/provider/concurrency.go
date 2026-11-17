package provider

import (
	"fmt"
	"strconv"
	"strings"
	"time"
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

// AtOnce is how many of the provider's accounts or keys take requests at
// the same time, 0 for no limit: the user's MaxAccounts when they set one,
// else what the plugin giving the provider says (its auth hook's
// maxAccounts, or its package.json's magpie.maxAccounts), else none. An
// account not had from All or AlsoOn carries none of the provider's
// settings: they are read from providers.json then.
func (p Provider) AtOnce() int {
	n := p.MaxAccounts
	if n == nil && p.Account != nil {
		if s, ok := storedPicks(p.ID); ok {
			n = s.MaxAccounts
		}
	}
	if n != nil {
		return min(max(*n, 0), MaxLimit)
	}
	return p.PluginAtOnce()
}

// PluginAtOnce is how many of its accounts the plugin giving the provider
// says may take requests at the same time, as it lists it now; 0 for none,
// or for a provider no plugin gives.
func (p Provider) PluginAtOnce() int {
	if !p.IsPlugin() {
		return 0
	}
	if cur, ok := PluginOf(p.ID); ok {
		return min(max(cur.MaxAccounts, 0), MaxLimit)
	}
	return min(max(p.Account.plugin.MaxAccounts, 0), MaxLimit)
}

// SetAtOnce sets how many of the provider's accounts or keys take requests
// at the same time: 0 for no limit, nil for what its plugin says.
func SetAtOnce(id string, n *int) error {
	if n != nil && (*n < 0 || *n > MaxLimit) {
		return fmt.Errorf("accounts at once is from 0 (no limit) to %d, not %d", MaxLimit, *n)
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.MaxAccounts = n
	return Save(*p)
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
// provider's (Concurrency). An account not had from All or AlsoOn (made
// afresh by its sign-in) carries none of the provider's settings: they are
// read from providers.json then.
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

// MaxImagesLimit bounds a limit on the images one request may carry. No
// vendor lists more than a few hundred, and a number past this is a typo
// rather than a cap.
const MaxImagesLimit = 1000

// normalMaxImages is a MaxImages as saved: model ids trimmed, each cap held
// to 1..MaxImagesLimit, a cap of 0 or less (none) and an empty id dropped.
func normalMaxImages(m map[string]int) map[string]int {
	var out map[string]int
	for id, n := range m {
		id = strings.TrimSpace(id)
		if id == "" || n <= 0 {
			continue
		}
		if out == nil {
			out = map[string]int{}
		}
		out[id] = min(n, MaxImagesLimit)
	}
	return out
}

// ImageCapOf is the most images one request to model may carry, as the
// user set it on p: the model's own, else the one for all its models, 0
// for none.
func (p Provider) ImageCapOf(model string) int {
	if n := p.MaxImages[model]; n > 0 {
		return n
	}
	return p.MaxImages["*"]
}

// RPMLimit is how many requests a minute each of p's keys or accounts may
// send the vendor, 0 for no limit: the provider's MaxRPM. An account not
// had from All or AlsoOn (made afresh by its sign-in) carries none of the
// provider's settings: it is read from providers.json then.
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

// MaxQuotaRest bounds QuotaRest, in minutes: eight days, a week's window
// and a day over, the longest the gateway rests anyone out of quota.
const MaxQuotaRest = 8 * 24 * 60

// QuotaRestFor is how long one of p's keys sits out of quota when the
// vendor doesn't say when it is back, 0 for the gateway's own.
func (p Provider) QuotaRestFor() time.Duration {
	return time.Duration(min(max(p.QuotaRest, 0), MaxQuotaRest)) * time.Minute
}

// SetQuotaRest sets how many minutes the provider's keys sit out of quota
// when the vendor doesn't say when they are back, 0 for the gateway's own.
func SetQuotaRest(id string, minutes int) error {
	if minutes < 0 || minutes > MaxQuotaRest {
		return fmt.Errorf("a rest out of quota is from 0 (magpie's own) to %d minutes (8 days), not %d", MaxQuotaRest, minutes)
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.QuotaRest = minutes
	if minutes > 0 {
		p.QuotaResetAt = "" // the one set last is the one that counts
	}
	return Save(*p)
}

// OwnQuotaRest is how long, from now, one of p's keys sits out of quota
// when the vendor doesn't say when it is back: until the next QuotaResetAt
// when one is set, else QuotaRest; 0 for the gateway's own.
func (p Provider) OwnQuotaRest(now time.Time) time.Duration {
	if t, ok := NextResetAt(p.QuotaResetAt, now); ok {
		return t.Sub(now)
	}
	return p.QuotaRestFor()
}

// ResetAt is a time of day an allowance comes back: an hour and minute, on
// this computer's clock (Loc nil) or at a fixed offset from UTC.
type ResetAt struct {
	Hour, Minute int
	Loc          *time.Location
}

// ParseResetAt reads a time of day as QuotaResetAt keeps it: "08:00",
// "8:30", "00:00+08:00", "16:00Z". "" is none, with no error.
func ParseResetAt(s string) (*ResetAt, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	bad := fmt.Errorf("a time of day is HH:MM, or HH:MM+08:00 on the vendor's clock, not %q", s)
	clock, zone := s, ""
	if i := strings.IndexAny(s, "+-Zz"); i >= 0 {
		clock, zone = s[:i], s[i:]
	}
	h, m, ok := strings.Cut(clock, ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || len(m) != 2 || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return nil, bad
	}
	r := &ResetAt{Hour: hh, Minute: mm}
	switch {
	case zone == "":
	case zone == "Z" || zone == "z":
		r.Loc = time.UTC
	default:
		t, err := time.Parse("-07:00", zone)
		if err != nil {
			return nil, bad
		}
		_, off := t.Zone()
		r.Loc = time.FixedZone(zone, off)
	}
	return r, nil
}

// NextResetAt is the first time after now that the time of day s names
// comes round; ok is false when s names none.
func NextResetAt(s string, now time.Time) (time.Time, bool) {
	r, err := ParseResetAt(s)
	if err != nil || r == nil {
		return time.Time{}, false
	}
	loc := r.Loc
	if loc == nil {
		loc = now.Location()
	}
	n := now.In(loc)
	t := time.Date(n.Year(), n.Month(), n.Day(), r.Hour, r.Minute, 0, 0, loc)
	if !t.After(now) {
		t = time.Date(n.Year(), n.Month(), n.Day()+1, r.Hour, r.Minute, 0, 0, loc)
	}
	return t, true
}

// SetQuotaResetAt sets the time of day the provider's keys out of quota
// are back, when the vendor doesn't say: "" for none. Setting one lets go
// of QuotaRest, which it would stand over.
func SetQuotaResetAt(id, at string) error {
	if _, err := ParseResetAt(at); err != nil {
		return err
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.QuotaResetAt = strings.TrimSpace(at)
	if p.QuotaResetAt != "" {
		p.QuotaRest = 0
	}
	return Save(*p)
}

// RestCapOff is RestCap set to off (`magpie provider rest-cap <id> off`):
// a key or account of the provider that fails without saying why — a
// relay's or an account pool's 5xx, a dropped connection — doesn't rest at
// all, and is asked again by the next request as if it hadn't failed
// (Kismet Alan on Discord: an aggregator's unknown errors fail now and
// then, and the next request often goes through). What the vendor said
// plainly still rests it as ever: out of quota or credit, rate limited,
// its key refused (401, 403), its model not served, its window full.
const RestCapOff = -1

// NoFailRest says p's keys and accounts don't rest after an unknown
// failure (RestCapOff).
func (p Provider) NoFailRest() bool { return p.RestCap == RestCapOff }

// RestCapOr is the most one of p's keys or accounts sits out while it keeps
// failing: the user's RestCap, else the gateway's own def (RestCapOff
// included: it says nothing of a rate limit's rest).
func (p Provider) RestCapOr(def time.Duration) time.Duration {
	if c := min(max(p.RestCap, 0), MaxQuotaRest); c > 0 {
		return time.Duration(c) * time.Minute
	}
	return def
}

// SetRestCap sets the most, in minutes, the provider's keys and accounts
// sit out while they keep failing or being rate limited, 0 for the
// gateway's own, RestCapOff for no rest after an unknown failure.
func SetRestCap(id string, minutes int) error {
	if minutes != RestCapOff && (minutes < 0 || minutes > MaxQuotaRest) {
		return fmt.Errorf("the longest rest is from 0 (magpie's own) to %d minutes (8 days), or off, not %d", MaxQuotaRest, minutes)
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.RestCap = minutes
	return Save(*p)
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
