package provider

// A sub2api key's own limits: its panel lets the owner give a key a
// spending limit per 5 hours, day and 7 days (rate_limit_5h/1d/7d), and
// once it has one, the key's /v1/usage answers in its quota_limited mode
// with each window's spend, limit and reset — and, unless the key has a
// total quota too, no "remaining" at all.

import (
	"encoding/json"
	"strings"
	"time"
)

// sub2APIWindows are the windows a sub2api key's rate_limits name, with
// the names magpie's cards already give a 5-hour, a day's and a week's.
var sub2APIWindows = []struct {
	window, name string
	span         time.Duration
}{
	{"5h", "5 hours", 5 * time.Hour},
	{"1d", "1 day", 24 * time.Hour},
	{"7d", "7 days", 7 * 24 * time.Hour},
}

// readSub2APIKeyLimits is the windows of a sub2api /v1/usage reply's
// rate_limits, in dollars: {"window":"7d","limit":800,"used":12.5,
// "remaining":787.5,"window_start":…,"reset_at":…}. A window with no
// reset_at hasn't started, or has run out since (sub2api says used 0 for
// it then, and gives no window a limit of 0): it starts on the key's next
// request, so it renews within its span. ok is false for a reply without them — a key on the wallet, or not
// sub2api's.
func readSub2APIKeyLimits(body []byte) (ws []QuotaWindow, ok bool) {
	var r struct {
		RateLimits []struct {
			Window  string     `json:"window"`
			Limit   float64    `json:"limit"`
			Used    float64    `json:"used"`
			ResetAt *time.Time `json:"reset_at"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil, false
	}
	for _, s := range sub2APIWindows {
		for _, l := range r.RateLimits {
			if strings.TrimSpace(l.Window) != s.window || l.Limit <= 0 {
				continue
			}
			w := QuotaWindow{Name: s.name, Span: s.span, Amount: max(0, l.Used), Limit: l.Limit, Unit: "USD"}
			w.Used = min(100, max(0, l.Used/l.Limit*100))
			if l.ResetAt != nil && !l.ResetAt.IsZero() {
				t := *l.ResetAt
				w.ResetsAt = &t
			}
			ws = append(ws, w)
			break
		}
	}
	return ws, len(ws) > 0
}
