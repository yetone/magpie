package provider

import "time"

// Pace is how far w's use, read at readAt, is from an even pace through
// the window at now, in points of the window (fflake33 on Discord): what
// is used less the share of the window gone by. +12 is twelve points more
// used than an even pace would have used by now, so at this rate the
// window runs out before it resets; -8 is eight points to spare. ok is
// false for a window with no limit, or whose start or reset isn't known,
// or when now is outside it.
func (w QuotaWindow) Pace(readAt, now time.Time) (pace float64, ok bool) {
	if w.Unlimited {
		return 0, false
	}
	start, end, ok := w.Bounds(readAt)
	if !ok || !end.After(start) || now.Before(start) || !now.Before(end) {
		return 0, false
	}
	even := 100 * float64(now.Sub(start)) / float64(end.Sub(start))
	return max(0, min(100, w.Used)) - even, true
}

// Length is how long w runs: as its vendor said or its name says
// (windowSpan), else, for an Antigravity model's own window, which says
// neither, the 5 hours fetchAvailableModels gives it (modelSpan); 0 when
// not known.
func (w QuotaWindow) Length() time.Duration {
	if s := windowSpan(w); s > 0 {
		return s
	}
	if w.Family != "" && w.Model != "" {
		return modelSpan
	}
	return 0
}

// WithWindowPeriods is qs with each window's Period set where it is known,
// for the Usage table's columns; qs and its windows, which the caches
// share, are left as they were.
func WithWindowPeriods(qs []SubscriptionQuota) []SubscriptionQuota {
	out := make([]SubscriptionQuota, len(qs))
	for i, q := range qs {
		if len(q.Windows) > 0 {
			ws := make([]QuotaWindow, len(q.Windows))
			for j, w := range q.Windows {
				w.Period = int64(w.Length() / time.Second)
				ws[j] = w
			}
			q.Windows = ws
		}
		out[i] = q
	}
	return out
}

// WithWindowStarts is qs with each window's StartsAt set where its bounds
// are known, for the GUI's Pace number; qs and its windows, which the
// caches share, are left as they were.
func WithWindowStarts(qs []SubscriptionQuota, now time.Time) []SubscriptionQuota {
	out := make([]SubscriptionQuota, len(qs))
	for i, q := range qs {
		readAt := now
		if q.ReadAt != nil && !q.ReadAt.After(now) {
			readAt = *q.ReadAt
		}
		if len(q.Windows) > 0 {
			ws := make([]QuotaWindow, len(q.Windows))
			for j, w := range q.Windows {
				if w.StartsAt == nil && !w.Unlimited {
					if start, _, ok := w.Bounds(readAt); ok {
						w.StartsAt = &start
					}
				}
				ws[j] = w
			}
			q.Windows = ws
		}
		out[i] = q
	}
	return out
}
