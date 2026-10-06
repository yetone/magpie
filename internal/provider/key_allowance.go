package provider

// A key's own usage windows, for routing, as Allowances is a subscription
// account's: a sub2api key its owner gave a 5-hour, day or 7-day limit
// tells what it has spent of each, and a GLM Coding Plan's key (Zhipu's
// or Z.ai's) its 5-hour and weekly windows, which the plan's monitor
// endpoint tells as the Usage page asks. Routing weighs and rests a key
// by them. They are read behind the request, never waited for, and read
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
}

// keyAllowanceAge is how long a key's windows as read go unasked again.
const keyAllowanceAge = time.Minute

// readsKeyWindows says p is a key whose own usage windows magpie reads:
// one whose Balance URL is a sub2api panel's query for a key, or a GLM
// Coding Plan's key, which the plan's monitor endpoint tells. Said
// without asking anything, so any other key costs nothing here.
func readsKeyWindows(p Provider) bool {
	return p.Account == nil && p.Key != "" && (sub2APIKeyLimits(p) || glmCodingPlanKey(p))
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
	if !e.loading && time.Since(e.at) > keyAllowanceAge {
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

// keyWindows asks the vendor for p's key's windows, as the key's card is
// asked (KeyBalances, PlanQuotas): the provider as saved, with the key,
// rather than as a request has it, which leaves out the base URLs of the
// protocols a key isn't made for. A GLM Coding Plan's key is asked where
// its card asks (the plan's monitor endpoint), a sub2api key where its
// Balance URL points.
func keyWindows(ctx context.Context, p Provider) ([]QuotaWindow, error) {
	q := p
	if saved, err := Find(p.ID); err == nil {
		q = *saved
		q.Key = p.Key
	}
	if glmCodingPlanKey(q) {
		src, _ := planQuotaSourceOf(q)
		_, ws, err := planWindows(q.Via(ctx), src, q.Key)
		return ws, err
	}
	_, _, ws, _, err := balanceRead(ctx, q)
	return ws, err
}

// lastKeyAllowance is p's key's windows as its card last read them, kept
// on disk (keepLast) — a balance's for a sub2api key, a plan's for a GLM
// Coding Plan's — each started again whose reset has passed.
func lastKeyAllowance(p Provider) Allowance {
	kind := "balance"
	if glmCodingPlanKey(p) {
		kind = "plan"
	}
	c := &lastQuotas
	c.Lock()
	defer c.Unlock()
	c.load()
	q, ok := c.reading(p.ID + "/" + strings.ToLower(keyTag(kind, p.Key)))
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
		e.at = time.Time{}
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
		e.at, e.loading = time.Time{}, false
	}
	c.gen++
	c.Unlock()
}
