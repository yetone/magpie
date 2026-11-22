package provider

import "time"

// Google's Code Assist quota (the agy CLI's /usage, and the Antigravity
// app's) answers from replicas that disagree for days after a reset: one
// reading tells the new window (started at the reset, a full share), the
// next the old one it replaced (started a week before, with what was left
// of it then), back and forth (SeniorZhai on X, 0.1.4657: "重置了，对起始
// 时间没有重置"; the owner's account flipped twelve times in twelve
// minutes on 11-17 between 5.05% left to 11-21 and 100% to 11-24, and
// again on 11-21). Each flip read as the window starting again, so the
// card's start and share, its pace, and the cycles the Usage page draws
// went back to the window the reset had ended.
//
// The vendor tells which window is current: its reset less its span is
// when it began, and a window never begins before one already read that
// hasn't ended. So a reading of an earlier window, while a later one read
// before still runs, is a stale replica's: it isn't kept, and the card
// shows the later window as last read. Scoped to Google's quota, whose
// replicas do this; another vendor's window that moves back is taken as
// it says.

// staleWindowVendors are the providers whose quota readings can tell a
// window a reset already replaced.
var staleWindowVendors = map[string]bool{"agy-cli": true, "antigravity": true, "antigravity-plugin": true}

// staleAfter is whether p, read after newest, is of a window that began
// before newest's did while newest's still runs: the reset newest tells
// had happened by then.
func staleAfter(newest, p QuotaPoint) bool {
	if newest.Start == nil || newest.ResetsAt == nil || p.Start == nil || p.ResetsAt == nil {
		return false
	}
	return !p.At.Before(newest.At) && p.Start.Before(newest.Start.Add(-quotaHistSlack)) &&
		newest.ResetsAt.After(p.At) && p.ResetsAt.After(p.At)
}

// withoutStale is pts, of a vendor's line that staleWindowVendors names,
// without the readings of a window a later one had replaced; pts itself,
// when there are none.
func withoutStale(provider string, pts []QuotaPoint) []QuotaPoint {
	if !staleWindowVendors[provider] {
		return pts
	}
	var out []QuotaPoint
	for i, p := range pts {
		if out == nil {
			if i == 0 || !staleAfter(pts[i-1], p) {
				continue
			}
			out = append(make([]QuotaPoint, 0, len(pts)), pts[:i]...)
			continue
		}
		if !staleAfter(out[len(out)-1], p) {
			out = append(out, p)
		}
	}
	if out == nil {
		return pts
	}
	return out
}

// currentWindow is w, a window of a reading at at, as the later window
// newest tells when w's reading is a stale one (staleAfter): its share
// and reset as last read. ok is false when w is current.
func currentWindow(w QuotaWindow, p, newest QuotaPoint) (QuotaWindow, bool) {
	if !staleAfter(newest, p) {
		return w, false
	}
	r := *newest.ResetsAt
	w.Used, w.ResetsAt, w.ResetSecs, w.StartsAt = 100-newest.Left, &r, 0, nil
	return w, true
}

// sameBucket is whether a model's window f reads the pool window w's
// bucket: the agy-cli plugin gives each model family its pool's tightest
// bucket, figures and reset alike.
func sameBucket(f, w QuotaWindow) bool {
	return f.Family != "" && w.Family == "" && f.Pool != "" && f.Pool == w.Pool && f.Used == w.Used &&
		windowSpan(f) == windowSpan(w) && sameReset(f.ResetsAt, w.ResetsAt)
}

func sameReset(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}
