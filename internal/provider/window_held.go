package provider

import "time"

// WithHeldWindows is qs with HeldBy set on each window whose remainder an
// account can't spend: another of its windows that stops it for every
// model (usedPast's rule: not aside, no model of its own, not past its
// reset) is used up and starts again only after this one does, so what
// this one has left is lost at its own reset, unspent. An account with 0%
// of its 7 days left and 100% of its 5 hours has no 5 hours to spend, and
// the Usage page's all-accounts strip, the tray panel's and the Routing
// groups' (mergeQuotas) count it as none left rather than as a full window
// (hayden4608, handong66: three such accounts and one with its 5 hours
// used up read 75% of 5 hours left in all, #4791). It isn't so the other
// way: a week with 22% left behind five hours used up still has that 22%
// once the five hours start again. A ChatGPT account that goes on on its
// credits isn't held. A pool's window (Antigravity's Claude & GPT, its
// models' shared allowance) holds its own pool's windows the same way, and
// no other: a Claude & GPT week used up leaves that pool's 5 hours nothing
// to spend, while Gemini's go on (kawa_tsura on X). Another magpie's cards
// are as it sent them; qs and its windows, which the caches share, are
// left as they were.
func WithHeldWindows(qs []SubscriptionQuota, now time.Time) []SubscriptionQuota {
	out := make([]SubscriptionQuota, len(qs))
	for i, q := range qs {
		out[i] = q
		if q.From != "" || q.Error != "" {
			continue
		}
		var stops []int
		for j, w := range q.Windows {
			if stopsAll(q, w, now) || stopsPool(w, now) {
				stops = append(stops, j)
			}
		}
		if len(stops) == 0 {
			continue
		}
		ws := make([]QuotaWindow, len(q.Windows))
		for j, w := range q.Windows {
			if !w.Unlimited && !w.Text && w.Balance == nil {
				for _, s := range stops {
					if s != j && (q.Windows[s].Pool == "" || q.Windows[s].Pool == w.Pool) && outlasts(q.Windows[s], w, now) {
						w.HeldBy = q.Windows[s].Name
						break
					}
				}
			}
			ws[j] = w
		}
		out[i].Windows = ws
	}
	return out
}

// stopsAll says whether w, one of q's windows, is used up at now and stops
// the account for every model while it is.
func stopsAll(q SubscriptionQuota, w QuotaWindow, now time.Time) bool {
	if w.Aside || w.Text || w.Unlimited || w.Model != "" || w.matches != nil || w.Used < 100 || resetPassed(w, now) {
		return false
	}
	return !w.Credits || !CodexCredits(q.User) // else the vendor spends the credits instead
}

// stopsPool says whether w, a pool's own window (no model of its own), is
// used up at now: it stops every model of its pool while it is.
func stopsPool(w QuotaWindow, now time.Time) bool {
	return w.Pool != "" && w.Model == "" && w.Family == "" && !w.Text && !w.Unlimited && w.Balance == nil && w.Used >= 100 && !resetPassed(w, now)
}

// outlasts says whether used-up window h starts again only after w does,
// by their resets where both are known, else by how long each runs.
func outlasts(h, w QuotaWindow, now time.Time) bool {
	hr, wr := heldReset(h, now), heldReset(w, now)
	if !hr.IsZero() && !wr.IsZero() {
		return hr.After(wr)
	}
	return windowSpan(h) > windowSpan(w)
}

// heldReset is when w starts again; zero when it doesn't say.
func heldReset(w QuotaWindow, now time.Time) time.Time {
	switch {
	case w.ResetsAt != nil:
		return *w.ResetsAt
	case w.ResetSecs > 0:
		return now.Add(time.Duration(w.ResetSecs) * time.Second)
	}
	return time.Time{}
}
