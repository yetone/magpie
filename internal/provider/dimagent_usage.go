package provider

// The allowance a DimAgent account reports: its credit buckets, the rate
// windows inside them, and any meter that counts something other than
// credits. magpie asks with the same access token its chats carry, and shows
// what the answer says, the way the vendor's own console draws it.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/dimagent"
)

func dimagentLoginQuota(ctx context.Context, l Login) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "dimagent", Name: "DimAgent", Icon: "dimagent",
		Plan: l.Plan, User: l.User, Windows: []QuotaWindow{}}
	c, err := dimagentFresh(ctx, l.User)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	body, err := dimagent.Get(ctx, dimagentClient, dimagentAPI+dimagent.UsagePath, c.Access)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	u, err := dimagent.ParseUsage(body)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	// The plan's name and paid term come on the subscription record; the
	// account's own token claims are the fallback for a reply that says neither.
	if u.Plan.Name != "" {
		q.Plan = u.Plan.Name
	}
	if u.Plan.RenewKnown {
		if u.Plan.Renew {
			q.Renew = "auto"
		} else {
			q.Renew = "off"
		}
	}
	if !u.Plan.EndsAt.IsZero() {
		at := u.Plan.EndsAt
		q.Until = &at
	}
	// A bucket is shown as the pot of credits it is, then as whatever rate
	// windows it runs its calls through.
	add := func(b dimagent.Bucket, name string) {
		switch {
		case b.Unlimited:
			q.Windows = append(q.Windows, QuotaWindow{Name: name, Display: "unlimited", Aside: true})
		case b.Total > 0:
			used := max(b.Used, 0) // a reply may owe more than it granted
			w := QuotaWindow{Name: name, Used: min(100, 100*used/b.Total),
				Display: fmt.Sprintf("%s / %s %s", compactNumber(used), compactNumber(b.Total), unitName(b))}
			if !b.ExpiresAt.IsZero() {
				at := b.ExpiresAt
				w.ResetsAt = &at
			}
			q.Windows = append(q.Windows, w)
		}
		for _, win := range b.Windows {
			if win.Cap <= 0 {
				continue
			}
			used := max(win.Used, 0)
			w := QuotaWindow{Name: dimagentWindowName(win.Hours), Used: min(100, 100*used/win.Cap),
				Span:    time.Duration(win.Hours * float64(time.Hour)),
				Display: fmt.Sprintf("%s / %s tokens", compactNumber(used), compactNumber(win.Cap))}
			if !win.Expires.IsZero() {
				at := win.Expires
				w.ResetsAt = &at
			}
			q.Windows = append(q.Windows, w)
		}
	}
	// The reader leaves out a tally that is only its buckets added up, so
	// whatever is kept here says something of its own.
	add(u.Total, "Credits")
	add(u.Subscription, "Credits")
	for _, a := range u.AddOns {
		add(a, "Add-on credits")
	}
	for _, m := range u.Meters {
		if m.Unlimited || m.Allowance <= 0 {
			continue
		}
		w := QuotaWindow{Name: meterName(m.Feature), Used: min(100, 100*m.Used/m.Allowance),
			Display: fmt.Sprintf("%s / %s %s", compactNumber(m.Used), compactNumber(m.Allowance), m.Unit)}
		if !m.PeriodEnd.IsZero() {
			at := m.PeriodEnd
			w.ResetsAt = &at
		}
		q.Windows = append(q.Windows, w)
	}
	if len(q.Windows) == 0 {
		q.Error = "DimAgent: the account reported no allowance"
	}
	return q
}

// dimagentWindowName is what a rate window is called, as long as it runs, in
// the words the app already uses for its other subscriptions' windows ("5
// hours", "7 days"), so the page's translations cover it. A window the app
// has no word for keeps the vendor's measure plain.
func dimagentWindowName(hours float64) string {
	if hours <= 0 {
		return "Window"
	}
	d := time.Duration(hours * float64(time.Hour))
	switch {
	case d%(24*time.Hour) == 0 && d >= 24*time.Hour:
		if days := int64(d / (24 * time.Hour)); days == 7 {
			return "7 days" // the one day count the app names
		} else {
			return fmt.Sprintf("%d days", days)
		}
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int64(d/time.Minute))
	default:
		return fmt.Sprintf("%g hours", hours)
	}
}

// unitName is what a bucket counts in, as the vendor names its currency —
// credits to a reader when the reply names none.
func unitName(b dimagent.Bucket) string {
	if u := strings.TrimSpace(b.Units); u != "" {
		return u
	}
	return "credits"
}

// meterName is a feature meter's label, spelled as a person would read it.
func meterName(feature string) string {
	switch feature {
	case "image":
		return "Images"
	case "video":
		return "Video"
	case "audio", "tts":
		return "Audio"
	case "search", "web_search":
		return "Searches"
	}
	if feature == "" {
		return "Usage"
	}
	return feature
}
