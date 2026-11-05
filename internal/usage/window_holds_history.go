package usage

import (
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// HistoricalWindowHolds reckons each window at its last reading in
// [from, to), not from today's percentage. cards supply the window's
// scope, never its current figures. Each returned card has one window,
// because the windows may have been sampled at different times.
// A shorter window is its last observed cycle, not all its cycles added.
func HistoricalWindowHolds(cards []provider.SubscriptionQuota, hist []provider.QuotaHistory, from, to time.Time) []provider.SubscriptionQuota {
	snapshots := []provider.SubscriptionQuota{}
	for _, q := range cards {
		if q.From != "" || q.User == "" {
			continue
		}
		for _, h := range hist {
			if h.Provider != q.Provider || !strings.EqualFold(h.User, q.User) {
				continue
			}
			for _, w := range q.Windows {
				if w.Pool != "" || w.Family != "" || w.Aside || w.Unlimited {
					continue
				}
				for _, l := range h.Lines {
					if l.Name != w.Name {
						continue
					}
					var last *provider.QuotaPoint
					for i := range l.Points {
						p := &l.Points[i]
						if !p.At.Before(from) && p.At.Before(to) {
							last = p
						}
					}
					if last == nil || last.Start == nil || last.ResetsAt == nil || !last.ResetsAt.After(*last.Start) {
						continue
					}
					w.Used, w.ResetsAt, w.ResetSecs, w.Holds, w.HoldsWhy, w.StartsAt = 100-last.Left, last.ResetsAt, 0, nil, "", nil
					// Use the recorded bounds, including windows whose span changed.
					w.Span = last.ResetsAt.Sub(*last.Start)
					// Balance stays: a card that tells one is never reckoned,
					// now or for a past cycle.
					snapshots = append(snapshots, provider.SubscriptionQuota{Provider: q.Provider, User: q.User, Balance: q.Balance, ReadAt: &last.At, Windows: []provider.QuotaWindow{w}})
				}
			}
		}
	}
	snapshots = withRecordedWindowStarts(snapshots, hist, to)
	since, ok := holdsSince(snapshots, to)
	if !ok {
		return snapshots
	}
	// No later reading or call can change what this historical range held.
	past := make([]provider.QuotaHistory, 0, len(hist))
	for _, h := range hist {
		a := provider.QuotaHistory{Provider: h.Provider, User: h.User}
		for _, l := range h.Lines {
			line := provider.QuotaLine{Name: l.Name}
			for _, p := range l.Points {
				if p.At.Before(to) {
					line.Points = append(line.Points, p)
				}
			}
			a.Lines = append(a.Lines, line)
		}
		past = append(past, a)
	}
	s := readLogSnapshot()
	return windowHolds(snapshots, to, s.firstOwn, apiPricer(), provider.UsageRenames(), provider.SamePlanAccounts(), dryRuns(snapshots, past, to), func(fn func(Record)) {
		s.visit(since, func(r Record) {
			if r.Time.Before(to) {
				fn(r)
			}
		})
	})
}

// An observed refill can begin a window later than reset minus span.
// Preserve that boundary when scaling its routed calls, on both live and
// historical cards. The provider's cached windows are never changed.
func withRecordedWindowStarts(cards []provider.SubscriptionQuota, hist []provider.QuotaHistory, now time.Time) []provider.SubscriptionQuota {
	out := make([]provider.SubscriptionQuota, len(cards))
	copy(out, cards)
	for i, q := range cards {
		at := readAtOf(q, now)
		cloned := false
		for j, w := range q.Windows {
			start, end, why := holdsBounds(q, w, at)
			if why != "" {
				continue
			}
			for _, h := range hist {
				if h.Provider != q.Provider || !strings.EqualFold(h.User, q.User) {
					continue
				}
				for _, l := range h.Lines {
					if l.Name != w.LineName() {
						continue
					}
					var observed time.Time
					for k, p := range l.Points {
						if p.At.After(at) {
							break
						}
						if k == 0 {
							continue // first seen is not a reset
						}
						if p.StartsCycleAfter(l.Points[k-1]) {
							observed = p.At
							if p.Start != nil {
								observed = *p.Start
							}
							// an early refill began between the two readings: at
							// the vendor's new start when it falls there, else
							// at the first reading of it; a reset that passed
							// began it no earlier than that reset
							prev := l.Points[k-1]
							if (prev.ResetsAt == nil || prev.ResetsAt.After(p.At)) && !(observed.After(prev.At) && !observed.After(p.At)) {
								observed = p.At
							}
							if prev.ResetsAt != nil && !prev.ResetsAt.After(p.At) && observed.Before(*prev.ResetsAt) {
								observed = *prev.ResetsAt
							}
						}
					}
					if observed.After(start) && observed.Before(at) && observed.Before(end) {
						if !cloned {
							out[i].Windows = append([]provider.QuotaWindow(nil), q.Windows...)
							cloned = true
						}
						out[i].Windows[j].Span = end.Sub(observed)
					}
				}
			}
		}
	}
	return out
}

// withSpansOf is out with each window's Span as it was in qs: a later
// observed start only narrows what is reckoned, never the window's Pace
// (provider.WithWindowStarts) or anything else read from the card.
func withSpansOf(out, qs []provider.SubscriptionQuota) []provider.SubscriptionQuota {
	for i := range out {
		if i >= len(qs) || len(out[i].Windows) != len(qs[i].Windows) {
			continue
		}
		cloned := false
		for j := range out[i].Windows {
			if out[i].Windows[j].Span == qs[i].Windows[j].Span {
				continue
			}
			if !cloned {
				out[i].Windows = append([]provider.QuotaWindow(nil), out[i].Windows...)
				cloned = true
			}
			out[i].Windows[j].Span = qs[i].Windows[j].Span
		}
	}
	return out
}
