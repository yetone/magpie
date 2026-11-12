package usage

// What a whole subscription window holds (Chiao on Discord): the Usage
// page's account cards show each window's share used, as the vendor reads
// it; magpie knows what it routed through the account since the window
// began, so the whole is about that over the share. It is told only where
// that is honest: the window's start and the account's calls are known,
// magpie's log reaches back to the start, and enough of it is used for a
// rounded percent to scale up (provider.HoldsFloor).

import (
	"math"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// magpie quota --json and GET /v1/magpie/quotas tell the same whole as the
// Usage page (#2333).
func init() { provider.ReckonHolds = WithWindowHolds }

// WithWindowHolds is qs with each window's Holds reckoned from the log, as
// it stood at now. qs itself, which the caller may share, is left as it is.
func WithWindowHolds(qs []provider.SubscriptionQuota, now time.Time) []provider.SubscriptionQuota {
	since, ok := holdsSince(qs, now)
	if !ok { // none can be reckoned: each one only says why
		return carryHolds(windowHolds(qs, now, time.Time{}, nil, provider.Renames{}, nil, nil, func(func(Record)) {}), now)
	}
	s := readLogSnapshot()
	hist := provider.QuotaHistories(since, "", "")
	started := withRecordedWindowStarts(qs, hist, now)
	dry := dryRuns(started, hist, now)
	out := windowHolds(started, now, s.firstOwn, apiPricer(), provider.UsageRenames(), provider.SamePlanAccounts(), dry, func(fn func(Record)) { s.visit(since, fn) })
	return carryHolds(withSpansOf(out, qs), now)
}

// holdsSince is the earliest start of a window that could be reckoned.
func holdsSince(qs []provider.SubscriptionQuota, now time.Time) (time.Time, bool) {
	var since time.Time
	found := false
	for _, q := range qs {
		at := readAtOf(q, now)
		for _, w := range q.Windows {
			if start, _, why := holdsBounds(q, w, at); why == "" && (!found || start.Before(since)) {
				since, found = start, true
			}
		}
	}
	return since, found
}

func readAtOf(q provider.SubscriptionQuota, now time.Time) time.Time {
	if q.ReadAt != nil && !q.ReadAt.After(now) {
		return *q.ReadAt
	}
	return now
}

// holdsBounds is the window of q's account to reckon w's whole over, where
// w can be: an account's own card, a window that runs out and starts again
// on a known clock, counts a whole account's or some named models' calls,
// and is used enough, and a reading not past its reset. why is empty where
// it can be, else the provider.Holds* reason it can't, or "-" for a window
// whose whole is never told (no reason worth a line on the card).
func holdsBounds(q provider.SubscriptionQuota, w provider.QuotaWindow, at time.Time) (start, end time.Time, why string) {
	if q.Error != "" || q.From != "" || q.User == "" || q.Balance != "" {
		return time.Time{}, time.Time{}, "-"
	}
	// An Antigravity pool's window is told, by its models' calls (max2020 on
	// Discord); the models' own windows drawing on it, and a pool whose
	// models aren't known, aren't.
	pool := w.PoolWindow() && len(provider.PoolMembers(q.Windows, w)) > 0
	// a wallet (Balance) has no whole: its Used, on a capped account, is
	// what it spent in the cap's period (provider's wallet_spend.go)
	if w.Unlimited || w.Aside && !pool || w.Family != "" || w.Pool != "" && !pool || w.Balance != nil || math.IsNaN(w.Used) {
		return time.Time{}, time.Time{}, "-"
	}
	if w.Used < provider.HoldsFloor {
		return time.Time{}, time.Time{}, provider.HoldsLow
	}
	start, end, ok := w.Bounds(at)
	if !ok {
		return time.Time{}, time.Time{}, provider.HoldsNoClock
	}
	if !end.After(at) || !start.Before(at) {
		return time.Time{}, time.Time{}, "-"
	}
	return start, end, ""
}

// dryRun is a stretch in which one of an account's windows had nothing
// left (#2010, NorethSea): from the last reading that still had some (zero
// when none is known) through the first that read none, to its known reset
// or the first reading that had some again, whichever is earlier (zero
// while neither is known). A call the vendor answered in it
// may have been paid from credits (Codex) or extra usage (Claude), not
// from the windows, and nothing in the log tells which.
type dryRun struct{ lastLeft, firstDry, refilled time.Time }

func (d dryRun) holds(t time.Time) bool {
	return !t.Before(d.lastLeft) && (d.refilled.IsZero() || t.Before(d.refilled))
}

// dryRuns is each account's dry runs, by provider and lowercased user,
// from its windows' kept readings and the card's own reading of them.
func dryRuns(qs []provider.SubscriptionQuota, hist []provider.QuotaHistory, now time.Time) map[string][]dryRun {
	out := map[string][]dryRun{}
	lines := map[string][]provider.QuotaPoint{} // account and window → readings, oldest first
	for _, h := range hist {
		for _, l := range h.Lines {
			lines[h.Provider+"\x00"+strings.ToLower(h.User)+"\x00"+l.Name] = l.Points
		}
	}
	for _, q := range qs {
		if q.User == "" {
			continue
		}
		at := readAtOf(q, now)
		for _, w := range q.Windows {
			if w.Unlimited || w.Aside || w.Family != "" || math.IsNaN(w.Used) {
				continue
			}
			k := q.Provider + "\x00" + strings.ToLower(q.User) + "\x00" + w.Name
			pts := lines[k]
			if n := len(pts); n == 0 || pts[n-1].At.Before(at) {
				p := provider.QuotaPoint{At: at, Left: max(0, 100-w.Used), ResetsAt: w.ResetsAt}
				if p.ResetsAt == nil && w.ResetSecs > 0 {
					end := at.Add(time.Duration(w.ResetSecs) * time.Second)
					p.ResetsAt = &end
				}
				pts = append(append([]provider.QuotaPoint(nil), pts...), p)
			}
			lines[k] = pts
		}
	}
	for k, pts := range lines {
		acct := k[:strings.LastIndex(k, "\x00")]
		var run *dryRun
		var lastLeft time.Time
		for _, p := range pts {
			// A poll may see the refill after the first call in the new
			// window. The known reset ends the dry run, not that poll, and
			// the window had some left from then: a run that starts at the
			// next reading of none goes back to the reset, not to the
			// log's start.
			if run != nil && !run.refilled.IsZero() && !p.At.Before(run.refilled) {
				out[acct] = append(out[acct], *run)
				run, lastLeft = nil, run.refilled
			}
			if p.Left <= 0 && p.ResetsAt != nil && !p.ResetsAt.After(p.At) {
				continue // a stale dry reading cannot extend an expired window
			}
			switch {
			case p.Left > 0 && run != nil:
				run.refilled = p.At
				out[acct] = append(out[acct], *run)
				run, lastLeft = nil, p.At
			case p.Left > 0:
				lastLeft = p.At
			case run == nil:
				run = &dryRun{lastLeft: lastLeft, firstDry: p.At}
			}
			if run != nil && p.ResetsAt != nil {
				run.refilled = *p.ResetsAt
			}
		}
		if run != nil {
			out[acct] = append(out[acct], *run)
		}
	}
	return out
}

// windowHolds fills in each window's Holds from the calls read gives: the
// calls of the card's provider (by the id it has now) answered by its
// account, to a model the window counts, from the window's start to when
// its share was read. first is the log's first call: a window begun before
// it has calls magpie can't see, and isn't reckoned. Nor is a window with a
// call in one of its account's dry runs that may have been paid otherwise:
// one where the vendor answered a call after a reading of none left, or
// where no reading with some left is known before it. same maps an account
// to the one whose card reads the plan it draws on (a ChatGPT API account
// to the same ChatGPT account in Codex, provider.SamePlanAccounts): its
// calls are counted as that one's.
func windowHolds(qs []provider.SubscriptionQuota, now, first time.Time, priceOf func(Record) *catalog.Price, renamed provider.Renames, same map[string]string, dry map[string][]dryRun, read func(func(Record))) []provider.SubscriptionQuota {
	type slot struct{ q, w int }
	type span struct{ start, end time.Time }
	want := map[slot]span{}
	why := map[slot]string{} // a window not reckoned, and why
	byAccount := map[string][]slot{}
	for i, q := range qs {
		at := readAtOf(q, now)
		for j, w := range q.Windows {
			start, _, no := holdsBounds(q, w, at)
			switch {
			case no == "-":
				continue
			case no != "":
				why[slot{i, j}] = no
				continue
			case first.IsZero() || first.After(start):
				why[slot{i, j}] = provider.HoldsBeforeLog
				continue
			}
			k := q.Provider + "\x00" + strings.ToLower(q.User)
			byAccount[k] = append(byAccount[k], slot{i, j})
			want[slot{i, j}] = span{start, at}
		}
	}
	if len(want) == 0 && len(why) == 0 {
		return qs
	}
	sums := map[slot]*provider.WindowHolds{}
	unpriced := map[slot]bool{}
	type runKey struct {
		account string
		run     int
	}
	paidOtherwise := map[runKey]bool{} // the vendor answered a call after a reading of none left
	inRun := map[slot][]runKey{}       // the dry runs a counted call fell in
	read(func(r Record) {
		if r.IsRejected() || r.IsReported() || r.Input+r.Output == 0 {
			return
		}
		// who answered, not who a merge shows it under (Renames.Own)
		r.Provider = renamed.Own(r.Provider, r.Time)
		// the call answers as the account it went out as, or, when it went
		// on a key (which names no account, #557), as the key: a plan's
		// card is on the key, named as the ledger names it (#3747)
		who := r.Account()
		if who == "" {
			who = r.ProviderKeyName
		}
		acct := r.Provider + "\x00" + strings.ToLower(who)
		if to, ok := same[acct]; ok {
			acct = to
		}
		var runs []runKey
		for i, d := range dry[acct] {
			if !d.holds(r.Time) {
				continue
			}
			k := runKey{acct, i}
			runs = append(runs, k)
			if d.lastLeft.IsZero() || r.Status < 400 && !r.Time.Before(d.firstDry) {
				paidOtherwise[k] = true
			}
		}
		for _, s := range byAccount[acct] {
			b := want[s]
			if r.Time.Before(b.start) || r.Time.After(b.end) || !qs[s.q].Windows[s.w].CountsIn(qs[s.q].Windows, r.Model) {
				continue
			}
			inRun[s] = append(inRun[s], runs...)
			h := sums[s]
			if h == nil {
				h = &provider.WindowHolds{Since: b.start}
				sums[s] = h
			}
			h.Routed.Calls++
			h.Routed.Tokens += int64(r.Input + r.Output)
			h.Routed.CacheRead += int64(r.CacheRead)
			h.Routed.CacheWrite += int64(r.CacheWrite)
			if pr := priceOf(r); pr != nil {
				h.Routed.Cost += r.CostAt(*pr)
				h.Routed.BaseCost += r.CostAt(baseRates(*pr))
			} else {
				unpriced[s] = true
			}
		}
	})
	out := make([]provider.SubscriptionQuota, len(qs))
	copy(out, qs)
	cloned := map[int]bool{}
	window := func(s slot) *provider.QuotaWindow {
		q := &out[s.q]
		if !cloned[s.q] { // the caller's windows are left as they are
			q.Windows, cloned[s.q] = append([]provider.QuotaWindow(nil), q.Windows...), true
		}
		return &q.Windows[s.w]
	}
	for s := range want {
		h := sums[s]
		switch {
		case h == nil || h.Routed.Tokens <= 0:
			why[s] = provider.HoldsNoneRouted
		case slices.ContainsFunc(inRun[s], func(k runKey) bool { return paidOtherwise[k] }):
			why[s] = provider.HoldsCredits
		}
	}
	for s, no := range why {
		window(s).HoldsWhy = no
	}
	for s, h := range sums {
		if why[s] != "" {
			continue
		}
		w := window(s)
		h.Used = w.Used
		scale := 100 / w.Used
		h.Tokens = int64(math.Round(float64(h.Routed.Tokens) * scale))
		if !unpriced[s] && h.Routed.Cost > 0 {
			h.Priced = true
			h.Cost = h.Routed.Cost * scale
			// told only where a premium was paid: the same figure twice
			// would ask the reader to tell them apart for nothing
			if math.Abs(h.Routed.Cost-h.Routed.BaseCost) > 1e-9 {
				h.BaseCost = h.Routed.BaseCost * scale
			} else {
				h.Routed.BaseCost = 0
			}
		} else {
			h.Routed.BaseCost = 0
		}
		w.Holds = h
	}
	return out
}

// baseRates is pr without its long-context tiers: every call at the base
// rates however large its input (WindowHolds.BaseCost, #5861). A peak
// hour's price is kept, as that is what the vendor's base rate is then.
func baseRates(pr catalog.Price) catalog.Price {
	pr.Tiers = nil
	return pr
}

// apiPricer is each call's model at its API list price: its maker's, else
// the provider's own list, else the maker's of the model the user said it
// is the same as (provider.SamePriceIn), never a price the user set for the
// subscription (which may well be nothing) nor a rate it bills at. A model
// none of those prices is priced as the model the reply named (Served), as
// the ledger is (#1498), and so is a call the vendor answered with another
// model (servedInstead, #5340) when that one has a price. A price of
// nothing at all is no API price.
func apiPricer() func(Record) *catalog.Price {
	st := settings.Load()
	wires := st.Wires()
	prices := map[[4]string]*catalog.Price{}
	one := func(providerID, model string) *catalog.Price {
		v, ok := provider.MakerPrice(model)
		if !ok || v.Cost(1e6, 1e6, 0, 0) == 0 {
			ok = false
			if p, err := provider.Find(providerID); err == nil {
				v, ok = p.ListPrice(model)
			}
		}
		if !ok || v.Cost(1e6, 1e6, 0, 0) == 0 { // the model the user said it is (#2344)
			v, ok = provider.SamePriceIn(st, providerID, model)
		}
		if !ok || v.Cost(1e6, 1e6, 0, 0) == 0 {
			return nil
		}
		pr := v
		return &pr
	}
	return func(r Record) *catalog.Price {
		served := strings.TrimSpace(r.Served)
		if served == r.Model {
			served = ""
		}
		id := r.priceID()
		k := [4]string{id, r.Model, served, r.Effort}
		if pr, ok := prices[k]; ok {
			return pr
		}
		// a call the vendor answered with another model is priced as the
		// model that ran (#5340), the asked-for one's price standing when
		// that one has none
		var pr *catalog.Price
		if servedInstead(wires, id, r.Model, r.Effort, served) {
			pr = one(id, served)
		}
		if pr == nil {
			pr = one(id, r.Model)
		}
		if pr == nil && served != "" {
			pr = one(id, served)
		}
		prices[k] = pr
		return pr
	}
}
