package provider

import (
	"strings"
	"time"
)

// ImageLane is the name of a ChatGPT account's image window (QuotaWindow.Lane).
// Drawing with gpt-image-2 counts against an allowance of its own, apart from
// Codex's 5-hour and weekly windows (#6160). /wham/usage has no image window
// (additional_rate_limits is null), but each reply of the images API tells
// it, a 200's too: x-codex-active-limit names the image limit and its
// window's headers say when it renews (#6412), which the gateway keeps
// (ImageHeard). A refusal, usage_limit_reached, rests the image model until
// the time it names, or that window's renewal (ImageRest). So the row says
// that time, or "unknown": never a share, as the share the headers tell
// stayed 0 on an account that had drawn.
const ImageLane = "images"

// ImageRest is, set by the gateway, when the image model of the ChatGPT
// account user (as its usage is read, Account.UsageAgent) is back, while the
// vendor's refusal keeps it resting; false when it is not.
var ImageRest func(agent, user string, now time.Time) (time.Time, bool)

// ImageHeard is, set by the gateway, the image window the ChatGPT account
// user's last reply of the images API told, while it has not renewed;
// false when none told one.
var ImageHeard func(agent, user string, now time.Time) (CodexLimit, bool)

// WithImageLane is qs with a row for the image allowance added to each
// ChatGPT account's card that was read: Text and Aside, so no share is told
// and routing never reads it, with ResetsAt when a refusal or a reply's
// headers named the time it comes back and no time otherwise. A card of
// another magpie is as it came; qs and its windows, which the caches share,
// are left as they were.
func WithImageLane(qs []SubscriptionQuota, now time.Time) []SubscriptionQuota {
	out := make([]SubscriptionQuota, len(qs))
	for i, q := range qs {
		out[i] = q
		if q.Provider != "codex" || q.From != "" || q.Error != "" || q.User == "" {
			continue
		}
		ws := make([]QuotaWindow, 0, len(q.Windows)+1)
		for _, w := range q.Windows {
			if w.Lane != ImageLane {
				ws = append(ws, w)
			}
		}
		w := QuotaWindow{Name: "Image generation", Lane: ImageLane, Text: true, Aside: true, Display: "Unknown"}
		if ImageHeard != nil {
			// the window the last drawing's reply told: its renewal, and
			// used up when it said so; "Available" while it was not
			if l, ok := ImageHeard("codex", strings.ToLower(q.User), now); ok {
				until := l.Resets
				w.Display, w.ResetsAt, w.ResetSecs = "Available", &until, int64(until.Sub(now)/time.Second)
				if l.Used >= 100 {
					w.Display = "Used up"
				}
			}
		}
		if ImageRest != nil {
			if until, ok := ImageRest("codex", strings.ToLower(q.User), now); ok {
				w.Display, w.ResetsAt, w.ResetSecs = "Used up", &until, int64(until.Sub(now)/time.Second)
			}
		}
		out[i].Windows = append(ws, w)
	}
	return out
}
