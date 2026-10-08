package provider

// A key's own usage windows, for routing, as Allowances is a subscription
// account's: a sub2api key its owner gave a 5-hour, day or 7-day limit
// tells what it has spent of each, as a plan bought with a key (Kimi
// Code, the GLM Coding Plan, MiniMax's, OpenCode Go, Command Code's:
// planquota.go) tells its plan's windows, and routing weighs and rests it
// by them (#1016). They are read behind the request, never waited for, and read
// again once a minute has passed; until the first reading is back, the
// one last shown on the key's card stands in for it. A reading that finds
// a window the key was out of no longer full — its limit raised in the
// panel, or its usage reset — tells OnRenewed's hooks, as a Codex reset
// spent does an account's, so what sat out for it comes back.

import (
	"context"
	"strings"
	"sync"
	"time"
)

var keyAllowances struct {
	sync.Mutex
	m map[string]*keyAllowance // provider id#key id
	// gen is bumped as they are all forgotten: a reading begun before
	// says nothing of the keys as they are now
	gen int
}

type keyAllowance struct {
	a       Allowance
	at      time.Time // when read; zero to read again at the next ask
	loading bool
	// stale: made stale (StaleKeyAllowance) while a reading was out, which
	// was asked before the key said it was out and is followed by another
	stale bool
}

// keyAllowanceAge is how long a key's windows as read go unasked again;
// keyNoWindowsAge one's that was read and has none — a pay-as-you-go key
// at a vendor that sells plans too — which is asked again only now and
// then, in case a plan was bought since.
const (
	keyAllowanceAge = time.Minute
	keyNoWindowsAge = 30 * time.Minute
)

// readsKeyWindows says p is a key whose own usage windows magpie reads:
// one whose Balance URL is a sub2api panel's query for a key, or one at a
// vendor whose keys tell their plan's windows (planQuotaSourceOf). Said
// without asking anything, so any other key costs nothing here.
func readsKeyWindows(p Provider) bool {
	if p.Account != nil || p.Key == "" {
		return false
	}
	if sub2APIKeyLimits(p) {
		return true
	}
	_, ok := planQuotaSourceOf(p)
	return ok
}

func keyAllowanceID(p Provider) string { return p.ID + "#" + keyID(p.Key) }

// KeyAllowanceID is what OnRenewed's hooks are told, as the user of agent
// "", of a key whose own windows magpie reads; empty for any other.
func KeyAllowanceID(p Provider) string {
	if !readsKeyWindows(p) {
		return ""
	}
	return keyAllowanceID(p)
}

// KeyAllowance is the provider's key in use's own usage windows as last
// read, asked for again in the background when that was over a minute
// ago; ok is false for a key without any, or one not read yet. It never
// waits: a key not read yet goes by the last reading on its card, else
// counts as not known.
func KeyAllowance(p Provider) (Allowance, bool) {
	if !readsKeyWindows(p) {
		return nil, false
	}
	id := keyAllowanceID(p)
	c := &keyAllowances
	c.Lock()
	_, had := c.m[id]
	c.Unlock()
	var seed Allowance
	if !had {
		seed = lastKeyAllowance(p) // read before the lock: it takes the cards'
	}
	c.Lock()
	defer c.Unlock()
	if c.m == nil {
		c.m = map[string]*keyAllowance{}
	}
	e := c.m[id]
	if e == nil {
		e = &keyAllowance{a: seed}
		c.m[id] = e
	}
	age := keyAllowanceAge
	if len(e.a) == 0 && !e.at.IsZero() {
		age = keyNoWindowsAge
	}
	if !e.loading && time.Since(e.at) > age {
		e.loading = true
		go readKeyAllowance(p, id, c.gen)
	}
	return e.a, len(e.a) > 0
}

// readKeyAllowance reads p's key's windows into the cache. A reading that
// fails keeps what was known, and is tried again a minute later: a failed
// read is not known, never empty.
func readKeyAllowance(p Provider, id string, gen int) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ws, err := keyWindows(ctx, p)
	now := time.Now()
	c := &keyAllowances
	c.Lock()
	e := c.m[id]
	if e == nil || c.gen != gen {
		c.Unlock()
		return // forgotten while it was read: read again
	}
	e.loading, e.at = false, now
	renewed := err == nil && e.keep(allowanceOf(ws, now), SpentShareOf(p.Routing), now)
	if e.stale {
		e.stale, e.loading, e.at = false, true, time.Time{}
		go readKeyAllowance(p, id, gen)
	}
	c.Unlock()
	if renewed {
		tellRenewed("", id)
	}
}

// keep has the key go by a, read at now, and says whether a window it
// was full in (at share, as routing counts it full) is full no more, or
// till sooner: its limit raised, its usage reset, or the window started
// again.
func (e *keyAllowance) keep(a Allowance, share float64, now time.Time) (renewed bool) {
	was := e.a.Full("", share, now)
	e.a, e.at = a, now
	return was.After(a.Full("", share, now))
}

// keyWindows asks the vendor for p's key's windows, as the key's card
// is asked (KeyBalances, PlanQuotas): the provider as saved, with the key,
// rather than as a request has it, which leaves out the base URLs of the
// protocols a key isn't made for.
func keyWindows(ctx context.Context, p Provider) ([]QuotaWindow, error) {
	q := p
	if saved, err := Find(p.ID); err == nil {
		q = *saved
		q.Key = p.Key
	}
	if src, ok := planQuotaSourceOf(q); ok && !sub2APIKeyLimits(q) {
		_, ws, _, err := planKeyWindows(q.Via(ctx), q, src, q.Key)
		return ws, err
	}
	_, _, ws, _, err := balanceRead(ctx, q)
	return ws, err
}

// keyCardTag is what p's key's card is kept on disk by (keepLast): its
// plan's card, or its balance's.
func keyCardTag(p Provider) string {
	if _, ok := planQuotaSourceOf(p); ok && !sub2APIKeyLimits(p) {
		return keyTag("plan", p.Key)
	}
	return keyTag("balance", p.Key)
}

// lastKeyAllowance is p's key's windows as its card last read them, kept
// on disk (keepLast), each started again whose reset has passed.
func lastKeyAllowance(p Provider) Allowance {
	c := &lastQuotas
	c.Lock()
	defer c.Unlock()
	c.load()
	q, ok := c.reading(p.ID + "/" + strings.ToLower(keyCardTag(p)))
	if !ok || len(q.Windows) == 0 {
		return nil
	}
	return allowanceOf(q.Windows, time.Now())
}

// noteKeyAllowance keeps the windows the key's card just read, for
// routing to go by at once.
func noteKeyAllowance(p Provider, ws []QuotaWindow, now time.Time) {
	if !readsKeyWindows(p) {
		return
	}
	c := &keyAllowances
	c.Lock()
	if c.m == nil {
		c.m = map[string]*keyAllowance{}
	}
	id := keyAllowanceID(p)
	e := c.m[id]
	if e == nil {
		e = &keyAllowance{}
		c.m[id] = e
	}
	renewed := e.keep(allowanceOf(ws, now), SpentShareOf(p.Routing), now)
	c.Unlock()
	if renewed {
		tellRenewed("", id)
	}
}

// StaleKeyAllowance has the next KeyAllowance ask the vendor again for
// p's key's windows rather than trust what it last said: the key just
// answered that it is out of them.
func StaleKeyAllowance(p Provider) {
	if !readsKeyWindows(p) {
		return
	}
	c := &keyAllowances
	c.Lock()
	if e := c.m[keyAllowanceID(p)]; e != nil {
		e.at, e.stale = time.Time{}, e.loading
	}
	c.Unlock()
}

// forgetKeyAllowances has every key's windows read again at its next ask:
// a key or its Balance URL changed (ForgetBalances). What was read stands
// until then — fresher than its card's, which routing's reads never write
// — and a key changed is another key, with windows of its own.
func forgetKeyAllowances() {
	c := &keyAllowances
	c.Lock()
	for _, e := range c.m {
		e.at, e.loading, e.stale = time.Time{}, false, false
	}
	c.gen++
	c.Unlock()
}
