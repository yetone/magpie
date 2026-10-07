package provider

// An account resting out of its allowance is back as soon as a reading of
// its windows finds the one it was out of started again, rather than when
// its rest runs out: a Claude account's rest is as long as Claude Code's
// refusal said, and a new /usage, what Claude Code says as it answers, or
// the reset passing (elapsed) can show the window renewed before then.
// Allowances, which routing reads each account's windows by, tells
// OnRenewed's hooks so, for every subscription: the built-ins' and a
// plugin's (as "plugin:<id>") alike.

import (
	"strings"
	"time"
)

// reading is an account's windows as Allowances last read them, and when.
type reading struct {
	a  Allowance
	at time.Time
}

// renewedFrom says whether a, read at now, finds the account full till
// sooner than was, read at wasAt, did (the rule a key's reading is told
// renewed by, keyAllowances' keep): for the whole account, or for a pool
// of some models only with the whole account's windows over it. A window
// used below share, or whose reset passed, is full no more; one full with
// its reset not known is full for good. Only the windows both readings
// have are weighed, so one not read this time tells nothing; nor does one
// read alike, nor one renewed while another over the same models is
// still full: the five hours started again in a week used up.
func (a Allowance) renewedFrom(was Allowance, wasAt time.Time, share float64, now time.Time) bool {
	type key struct{ name, model string }
	keyOf := func(l Limit) key { return key{l.name, l.Model} }
	read := map[key]bool{}
	for _, l := range a {
		read[keyOf(l)] = true
	}
	whole := func(l Limit) bool { return l.Model == "" && l.matches == nil }
	sooner := func(in func(Limit) bool) bool {
		in2 := func(l Limit) bool { return read[keyOf(l)] && in(l) }
		return was.fullTill(in2, share, wasAt).After(a.fullTill(in2, share, now))
	}
	if sooner(whole) {
		return true
	}
	for _, w := range was {
		if k := keyOf(w); !whole(w) && sooner(func(l Limit) bool { return whole(l) || keyOf(l) == k }) {
			return true
		}
	}
	return false
}

// fullTill is when the windows in leaves full at share at now renew: the
// last of their resets, zero when none is full, and far off when one is
// full with its reset not known.
func (a Allowance) fullTill(in func(Limit) bool, share float64, now time.Time) time.Time {
	var t time.Time
	for _, l := range a {
		switch {
		case !in(l) || l.Used < share:
		case l.Resets.IsZero():
			return time.Unix(1<<62, 0)
		case l.Resets.After(now) && l.Resets.After(t):
			t = l.Resets
		}
	}
	return t
}

// renewalShare is the share at which routing counts the windows of
// agent's accounts full (SpentShareOf its provider's routing): a plugin's
// ("plugin:<id>") by the provider its accounts are listed under.
func renewalShare(agent string) float64 {
	id := agent
	if p, ok := strings.CutPrefix(agent, "plugin:"); ok {
		id = PluginID(p)
	}
	share, _ := loginSwitching(id)
	return share
}
