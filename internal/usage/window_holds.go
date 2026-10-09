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
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// WithWindowHolds is qs with each window's Holds reckoned from the log, as
// it stood at now. qs itself, which the caller may share, is left as it is.
func WithWindowHolds(qs []provider.SubscriptionQuota, now time.Time) []provider.SubscriptionQuota {
	since, ok := holdsSince(qs, now)
	if !ok {
		return qs
	}
	s := readLogSnapshot()
	return windowHolds(qs, now, s.first, apiPricer(), provider.Renamed(), func(fn func(Record)) { s.visit(since, fn) })
}

// holdsSince is the earliest start of a window that could be reckoned.
func holdsSince(qs []provider.SubscriptionQuota, now time.Time) (time.Time, bool) {
	var since time.Time
	found := false
	for _, q := range qs {
		at := readAtOf(q, now)
		for _, w := range q.Windows {
			if start, _, ok := holdsBounds(q, w, at); ok && (!found || start.Before(since)) {
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

// holdsBounds is the window of q's account to reckon w's whole over, ok
// only where w can be: an account's own card, a window that runs out and
// starts again on a known clock, counts a whole account's or some named
// models' calls, and is used enough, and a reading not past its reset.
func holdsBounds(q provider.SubscriptionQuota, w provider.QuotaWindow, at time.Time) (time.Time, time.Time, bool) {
	if q.Error != "" || q.From != "" || q.User == "" || q.Balance != "" {
		return time.Time{}, time.Time{}, false
	}
	if w.Unlimited || w.Aside || w.Family != "" || w.Pool != "" || math.IsNaN(w.Used) || w.Used < provider.HoldsFloor {
		return time.Time{}, time.Time{}, false
	}
	start, end, ok := w.Bounds(at)
	if !ok || !end.After(at) || !start.Before(at) {
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

// windowHolds fills in each window's Holds from the calls read gives: the
// calls of the card's provider (by the id it has now) answered by its
// account, to a model the window counts, from the window's start to when
// its share was read. first is the log's first call: a window begun before
// it has calls magpie can't see, and isn't reckoned.
func windowHolds(qs []provider.SubscriptionQuota, now, first time.Time, priceOf func(Record) *catalog.Price, renamed map[string]string, read func(func(Record))) []provider.SubscriptionQuota {
	type slot struct{ q, w int }
	type span struct{ start, end time.Time }
	want := map[slot]span{}
	byAccount := map[string][]slot{}
	for i, q := range qs {
		at := readAtOf(q, now)
		for j, w := range q.Windows {
			start, _, ok := holdsBounds(q, w, at)
			if !ok || first.IsZero() || first.After(start) {
				continue
			}
			k := q.Provider + "\x00" + strings.ToLower(q.User)
			byAccount[k] = append(byAccount[k], slot{i, j})
			want[slot{i, j}] = span{start, at}
		}
	}
	if len(want) == 0 {
		return qs
	}
	sums := map[slot]*provider.WindowHolds{}
	unpriced := map[slot]bool{}
	read(func(r Record) {
		if r.IsRejected() || r.Input+r.Output == 0 {
			return
		}
		if next, ok := renamed[r.Provider]; ok {
			r.Provider = next
		}
		for _, s := range byAccount[r.Provider+"\x00"+strings.ToLower(r.Account())] {
			b := want[s]
			if r.Time.Before(b.start) || r.Time.After(b.end) || !qs[s.q].Windows[s.w].Counts(r.Model) {
				continue
			}
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
			} else {
				unpriced[s] = true
			}
		}
	})
	if len(sums) == 0 {
		return qs
	}
	out := make([]provider.SubscriptionQuota, len(qs))
	copy(out, qs)
	cloned := map[int]bool{}
	for s, h := range sums {
		if h.Routed.Tokens <= 0 {
			continue
		}
		q := &out[s.q]
		if !cloned[s.q] { // the caller's windows are left as they are
			q.Windows, cloned[s.q] = append([]provider.QuotaWindow(nil), q.Windows...), true
		}
		w := &q.Windows[s.w]
		h.Used = w.Used
		scale := 100 / w.Used
		h.Tokens = int64(math.Round(float64(h.Routed.Tokens) * scale))
		if !unpriced[s] && h.Routed.Cost > 0 {
			h.Priced = true
			h.Cost = h.Routed.Cost * scale
		}
		w.Holds = h
	}
	return out
}

// apiPricer is each call's model at its API list price: its maker's, else
// the provider's own list, never a price the user set for the
// subscription (which may well be nothing) nor a rate it bills at. A price
// of nothing at all is no API price.
func apiPricer() func(Record) *catalog.Price {
	prices := map[[2]string]*catalog.Price{}
	return func(r Record) *catalog.Price {
		k := [2]string{r.Provider, r.Model}
		if pr, ok := prices[k]; ok {
			return pr
		}
		var pr *catalog.Price
		v, ok := provider.MakerPrice(r.Model)
		if !ok || v.Cost(1e6, 1e6, 0, 0) == 0 {
			ok = false
			if p, err := provider.Find(r.Provider); err == nil {
				v, ok = p.ListPrice(r.Model)
			}
		}
		if ok && v.Cost(1e6, 1e6, 0, 0) > 0 {
			pr = &v
		}
		prices[k] = pr
		return pr
	}
}
