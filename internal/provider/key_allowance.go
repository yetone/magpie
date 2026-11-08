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
	"errors"
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
	aAt     time.Time // when a was read, for a window that started again
	at      time.Time // when last read, either way; zero to read again at the next ask
	loading bool
	// stale: made stale (StaleKeyAllowance) while a reading was out, which
	// was asked before the key said it was out and is followed by another
	stale bool
	// refused: the key said it was out (StaleKeyAllowance), so it is read
	// again even while allowances are read only when asked (#1518)
	refused bool
	// read is what routing last read of the key, for its card
	// (withKeyReadings), which only the Usage page reads
	read keyReading
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
	if _, ok := planQuotaSourceOf(p); ok {
		return true
	}
	return accountPlanOf(p) != ""
}

// accountPlanOf is which plan p's key is on whose windows the key's
// account tells, not the key: Volcengine Ark's Coding and Agent Plans and
// Tencent Cloud's Token Plan Enterprise, read with the account's access
// key (volcengine_usage.go, tencent_usage.go), and StepFun's Step Plan,
// read with magpie's sign-in to its platform (stepfun_plan.go). The
// Usage page had their windows while routing had none, and the Routing
// page said "one key" of a plan with them (chen on Discord). "" for any
// other key, or one whose access key magpie isn't given; said without
// asking anything or reading a file, as a request routes by it.
func accountPlanOf(p Provider) string {
	if p.Account != nil || p.Key == "" {
		return ""
	}
	keyed := p.AccessKeyID != "" && p.SecretAccessKey != ""
	if _, _, _, ok := volcPlanOf(p); ok && keyed {
		return "volc"
	}
	if _, ok := tencentPlanRegion(p); ok && keyed {
		return "tencent"
	}
	if StepFunSite(p) != "" {
		return "stepfun"
	}
	return ""
}

// accountPlanWindows reads q's account's plan windows, as its card on the
// Usage page reads them. A Tencent account with several team plans has
// no windows here: which one a key is of isn't told.
func accountPlanWindows(ctx context.Context, q Provider) (keyReading, error) {
	var c SubscriptionQuota
	switch accountPlanOf(q) {
	case "volc":
		action, plan, region, _ := volcPlanOf(q)
		c = volcPlanQuota(q.Via(ctx), q, action, plan, region)
	case "tencent":
		region, _ := tencentPlanRegion(q)
		cs := tencentPlanCards(ctx, q, region, time.Now)
		if len(cs) != 1 {
			return keyReading{}, nil
		}
		c = cs[0]
	case "stepfun":
		if !StepFunSignedIn(q.ID) {
			return keyReading{}, nil
		}
		c = stepPlanQuota(q.Via(ctx), q)
	}
	if c.Error != "" {
		return keyReading{}, errors.New(c.Error)
	}
	return keyReading{ws: c.Windows, plan: c.Plan}, nil
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
	// with allowances read only when asked, routing goes by what was read
	// last till the user asks, or the key says it is out (#1518)
	if !e.loading && time.Since(e.at) > age && (e.refused || !readsAsked()) {
		e.loading = true
		gen := c.gen
		goRead(func() { readKeyAllowance(p, id, gen) })
	}
	return e.a, len(e.a) > 0
}

// readKeyAllowance reads p's key's windows into the cache. A reading that
// fails keeps what was known, and is tried again a minute later: a failed
// read is not known, never empty.
func readKeyAllowance(p Provider, id string, gen int) {
	ctx := context.Background()
	c := &keyAllowances
	c.Lock()
	if e := c.m[id]; e != nil && e.refused {
		e.refused, ctx = false, unheld(ctx)
	}
	c.Unlock()
	ctx, seq := quotaReading(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, err := keyWindows(ctx, p)
	ws := r.ws
	now := time.Now()
	c.Lock()
	e := c.m[id]
	if e == nil || c.gen != gen {
		c.Unlock()
		return // forgotten while it was read: read again
	}
	e.loading, e.at = false, now
	var renewed, started bool
	if err == nil {
		renewed, started = e.keep(allowanceOf(ws, now), SpentShareOf(p.Routing), now)
	}
	if err == nil {
		r.at, r.seq = now, seq
		e.read = r
	}
	if e.stale {
		e.stale, e.loading, e.at = false, true, time.Time{}
		goRead(func() { readKeyAllowance(p, id, gen) })
	}
	c.Unlock()
	if renewed {
		tellRenewed("", id)
	}
	if started {
		tellStarted("", id)
	}
}

// keep has the key go by a, read at now, and says whether a window it
// was full in (at share, as routing counts it full) is full no more, or
// till sooner — its limit raised, its usage reset, or the window started
// again — and whether one stands started again in fact, its reset gone
// by or a new, later cycle (what the vendor itself timed waits for,
// #2718).
func (e *keyAllowance) keep(a Allowance, share float64, now time.Time) (renewed, started bool) {
	old, wasAt := e.a, e.aAt
	was := old.Full("", share, now)
	e.a, e.aAt = a, now
	if was.After(a.Full("", share, now)) {
		renewed = true
	}
	started = a.startedAgain(old, wasAt, share, now)
	return renewed, started
}

// keyWindows asks the vendor for p's key's windows, as the key's card
// is asked (KeyBalances, PlanQuotas): the provider as saved, with the key,
// rather than as a request has it, which leaves out the base URLs of the
// protocols a key isn't made for.
func keyWindows(ctx context.Context, p Provider) (keyReading, error) {
	q := p
	if saved, err := Find(p.ID); err == nil {
		q = *saved
		q.Key = p.Key
	}
	var r keyReading
	var err error
	if accountPlanOf(q) != "" && !sub2APIKeyLimits(q) {
		if _, ok := planQuotaSourceOf(q); !ok {
			return accountPlanWindows(ctx, q)
		}
	}
	if src, ok := planQuotaSourceOf(q); ok && !sub2APIKeyLimits(q) {
		r.plan, r.ws, _, err = planKeyWindows(q.Via(ctx), q, src, q.Key)
		return r, err
	}
	var parts []BalancePart
	r.balance, parts, r.split, r.ws, _, _, err = balanceRead(ctx, q)
	r.parts = cardParts(parts)
	return r, err
}

// keyCardTag is what p's key's card is kept on disk by (keepLast): its
// plan's card, or its balance's.
func keyCardTag(p Provider) string {
	if _, ok := planQuotaSourceOf(p); !ok && !sub2APIKeyLimits(p) {
		// an account's plan is its own card, never a key's
		// (withKeyReadings): Volcengine's the access key's plan's
		// (volcPlanQuotas), named for the first provider on it
		switch accountPlanOf(p) {
		case "volc":
			action, _, _, _ := volcPlanOf(p)
			return keyTag("volc "+action, p.AccessKeyID)
		case "tencent":
			return keyTag("tencent", p.AccessKeyID)
		case "stepfun":
			return keyTag("stepfun", p.ID)
		}
	}
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
	renewed, started := e.keep(allowanceOf(ws, now), SpentShareOf(p.Routing), now)
	e.at = now
	c.Unlock()
	if renewed {
		tellRenewed("", id)
	}
	if started {
		tellStarted("", id)
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
		e.refused = readsAsked()
	}
	c.Unlock()
}

// forgetKeyAllowances has every key's windows read again at its next ask:
// a key or its Balance URL changed (ForgetBalances). What was read stands
// until then — fresher than its card's, which routing's reads never write
// (they are laid over it only for another magpie, withKeyReadings) — and
// a key changed is another key, with windows of its own.
func forgetKeyAllowances() {
	c := &keyAllowances
	c.Lock()
	for _, e := range c.m {
		e.at, e.loading, e.stale = time.Time{}, false, false
	}
	c.gen++
	c.Unlock()
}
