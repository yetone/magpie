package access

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// Limit is a gateway key's budget for a window: a calendar one in local
// time (#585), a day from midnight, a week from Monday's midnight, a
// month from the 1st's; or a cycle of Days days (#1509, period "days")
// or of Hours hours (#2102, period "hours", a subscription's 5-hour
// window's pace), the first from when the limit was set, each next one a
// cycle after the last. Tokens counts each call's uncached input, output (reasoning
// included) and cache writes, and its cache reads too when CacheReads is
// set; Cost is an estimate in US dollars at the prices magpie shows on
// the Usage page, not a vendor's bill. A cap of 0 is no cap; a limit with
// neither is no limit.
type Limit struct {
	Period string `json:"period"`
	// Days is a "days" limit's cycle, 1 to MaxDays; 0 for the others.
	Days int `json:"days,omitempty"`
	// Hours is an "hours" limit's cycle, 1 to MaxHours; 0 for the others.
	Hours      int     `json:"hours,omitempty"`
	Tokens     int64   `json:"tokens,omitempty"`
	Cost       float64 `json:"cost,omitempty"`
	CacheReads bool    `json:"cacheReads,omitempty"`
	// Since is magpie's, never the caller's (Update sets it): for "days"
	// and "hours", when the first cycle began, the limit set or last reset; for a
	// calendar period, when it was last reset, so a call before it in
	// the same window doesn't count. Zero for a calendar limit never
	// reset, as every limit from before #1509 is.
	Since time.Time `json:"since,omitzero"`
	// Models scope the limit to some models (zola_xynb on X: a cap for
	// one model or one provider under one key): "<provider>/<model>" or
	// "<provider>/*", as a key's models are written. The limit counts
	// only the key's calls a model of them answered, and holds only
	// requests that would go to one: a routing group goes on to its
	// members outside it. None, the whole key, as every limit from
	// before this.
	Models []string `json:"models,omitempty"`
}

// Scoped: l counts only some models' calls (Limit.Models).
func (l *Limit) Scoped() bool { return l != nil && len(l.Models) > 0 }

// Scope is l's models as one string, the same for the same models in any
// order or letter case; "" for the whole key.
func (l *Limit) Scope() string {
	if !l.Scoped() {
		return ""
	}
	ms := make([]string, len(l.Models))
	for i, m := range l.Models {
		ms[i] = strings.ToLower(m)
	}
	slices.Sort(ms)
	return strings.Join(ms, ",")
}

// MaxLimits is how many limits one key can have (#2227).
const MaxLimits = 5

// MaxLimitModels is how many models one limit can count.
const MaxLimitModels = 50

// Limits are k's limits, its Limit first; none for a key with no limit.
// Each holds the key on its own: a request passes only while all of them
// have something left.
func (k Key) Limits() []*Limit { return limitsOf(k.Limit, k.More) }

// Limits are the limits who's key holds it to, as Key.Limits.
func (who Identity) Limits() []*Limit { return limitsOf(who.Limit, who.More) }

func limitsOf(first *Limit, more []Limit) []*Limit {
	var out []*Limit
	if first.Limited() {
		out = append(out, first)
	}
	for i := range more {
		if more[i].Limited() {
			out = append(out, &more[i])
		}
	}
	return out
}

// limitList is a copy of k's limits, to change and set back.
func (k *Key) limitList() []Limit {
	var out []Limit
	for _, l := range k.Limits() {
		out = append(out, *l)
	}
	return out
}

// setLimits keeps ls as k's limits: the first as Limit, so a magpie from
// before #2227 still holds the key to it, the rest as More.
func (k *Key) setLimits(ls []Limit) {
	k.Limit, k.More = nil, nil
	if len(ls) > 0 {
		first := ls[0]
		k.Limit = &first
		k.More = slices.Clone(ls[1:])
	}
	if len(k.More) == 0 {
		k.More = nil
	}
}

// sameWindow: l and o count in the same window, so two limits of one key
// that do are one limit with two caps.
func (l *Limit) sameWindow(o *Limit) bool {
	return l.Period == o.Period && l.Days == o.Days && l.Hours == o.Hours
}

// sameLimit: l and o count the same calls in the same window, so a key
// with both would have one limit twice. Limits of other models in one
// window are each their own.
func (l *Limit) sameLimit(o *Limit) bool {
	return l.sameWindow(o) && l.Scope() == o.Scope()
}

// Periods are the windows a limit can be set for.
var Periods = []string{"day", "week", "month", "days", "hours"}

// MaxDays is the longest "days" cycle, ten years.
const MaxDays = 3650

// MaxHours is the longest "hours" cycle, a year; a longer one is a
// "days" cycle.
const MaxHours = 8760

// cycle is a "days" or "hours" limit's cycle, 0 for a calendar one.
func (l *Limit) cycle() time.Duration {
	switch l.Period {
	case "days":
		return time.Duration(min(max(l.Days, 1), MaxDays)) * 24 * time.Hour
	case "hours":
		return time.Duration(min(max(l.Hours, 1), MaxHours)) * time.Hour
	}
	return 0
}

// Limited: l caps something.
func (l *Limit) Limited() bool { return l != nil && (l.Tokens > 0 || l.Cost > 0) }

// Valid is l as it is kept: nil for no limit, an error for one that
// can't be. Since is left as it came; Update decides it.
func (l *Limit) Valid() (*Limit, error) {
	if l == nil {
		return nil, nil
	}
	if l.Tokens < 0 || l.Cost < 0 || math.IsNaN(l.Cost) || math.IsInf(l.Cost, 0) {
		return nil, errors.New("A limit can't be negative")
	}
	if !l.Limited() {
		return nil, nil
	}
	v := *l
	if v.Period == "" {
		v.Period = "day"
	}
	switch v.Period {
	case "day", "week", "month":
		v.Days, v.Hours = 0, 0
	case "days":
		v.Hours = 0
		if v.Days < 1 || v.Days > MaxDays {
			return nil, fmt.Errorf("A limit's cycle is a whole number of days from 1 to %d", MaxDays)
		}
	case "hours":
		v.Days = 0
		if v.Hours < 1 || v.Hours > MaxHours {
			return nil, fmt.Errorf("A limit's cycle is a whole number of hours from 1 to %d", MaxHours)
		}
	default:
		return nil, errors.New("A limit's period is day, week, month, every N days or every N hours")
	}
	v.Cost = math.Round(v.Cost*1e4) / 1e4
	ms, err := CleanModels(v.Models)
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		if strings.HasPrefix(strings.ToLower(m), "group/") || strings.HasPrefix(strings.ToLower(m), "auto/") {
			return nil, errors.New("A limit counts models or providers, like openai/* or openai/gpt-5, not a routing group: " + m)
		}
	}
	if len(ms) > MaxLimitModels {
		return nil, fmt.Errorf("A limit can count at most %d models", MaxLimitModels)
	}
	v.Models = ms
	return &v, nil
}

// Per is the window as a sentence ends it: "day", "week", "month",
// "10-day cycle" or "5-hour cycle".
func (l *Limit) Per() string {
	switch l.Period {
	case "days":
		return fmt.Sprintf("%d-day cycle", l.Days)
	case "hours":
		return fmt.Sprintf("%d-hour cycle", l.Hours)
	}
	return l.Period
}

// Counts says a call model answered ("<provider>/<model>") counts toward
// l: every call for a limit of the whole key.
func (l *Limit) Counts(model string) bool {
	return !l.Scoped() || ModelAllowed(l.Models, model)
}

// sinceOf is the Since a limit set as next keeps, with prev the key's
// limit before: its own while the period and cycle stay the same (a new
// cap doesn't restart the window), else a new cycle from now for "days"
// and "hours", and none for a calendar period.
func sinceOf(prev, next *Limit, now time.Time) time.Time {
	cyclic := next.cycle() > 0
	if prev != nil && prev.Period == next.Period && prev.Days == next.Days && prev.Hours == next.Hours && (!cyclic || !prev.Since.IsZero()) {
		return prev.Since
	}
	if cyclic {
		return now
	}
	return time.Time{}
}

// epoch is where a cycle written by hand without a Since runs from.
var epoch = time.Unix(0, 0)

// Window is the window l counts in at now, in now's location: when it
// began and when the next begins. A "days" or "hours" cycle is counted
// in absolute time, 24h a day whatever the clocks do, so a restart or a change of
// time zone or summer time keeps it; one with no Since runs from the Unix
// epoch. A calendar window starts at Since instead when the key was reset
// in it.
func (l *Limit) Window(now time.Time) (start, reset time.Time) {
	loc := now.Location()
	if n := l.cycle(); n > 0 {
		anchor := l.Since
		if anchor.Before(epoch) {
			anchor = epoch
		}
		if now.Before(anchor) { // the clock was set back past the start
			return anchor.In(loc), anchor.Add(n).In(loc)
		}
		start = anchor.Add(now.Sub(anchor) / n * n)
		return start.In(loc), start.Add(n).In(loc)
	}
	start, reset = Window(l.Period, now)
	if l.Since.After(start) && l.Since.Before(reset) {
		start = l.Since.In(loc)
	}
	return start, reset
}

// Window is the calendar window of period that now is in, in now's
// location: its start and when the next begins.
func Window(period string, now time.Time) (start, reset time.Time) {
	y, m, d := now.Date()
	loc := now.Location()
	switch period {
	case "week":
		back := (int(now.Weekday()) + 6) % 7 // days since Monday
		start = time.Date(y, m, d-back, 0, 0, 0, 0, loc)
		return start, time.Date(y, m, d-back+7, 0, 0, 0, 0, loc)
	case "month":
		return time.Date(y, m, 1, 0, 0, 0, 0, loc), time.Date(y, m+1, 1, 0, 0, 0, 0, loc)
	default:
		return time.Date(y, m, d, 0, 0, 0, 0, loc), time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	}
}
