package provider

import (
	"context"
	"math"
	"slices"
	"time"
)

// Quotas is what is left everywhere magpie can ask: the signed-in
// subscriptions' windows, the plans bought with a key, and the keys'
// balances, as the Usage page shows them. The vendors are asked at once;
// what doesn't answer before ctx ends comes back with an error.
func Quotas(ctx context.Context) []SubscriptionQuota {
	subs, plans, balances := quotas(ctx)
	return append(append(subs, plans...), balances...)
}

func quotas(ctx context.Context) (subs, plans, balances []SubscriptionQuota) {
	b := make(chan []SubscriptionQuota, 1)
	p := make(chan []SubscriptionQuota, 1)
	go func() { b <- KeyBalances(ctx) }()
	go func() { p <- PlanQuotas(ctx) }()
	subs = SubscriptionUsage(ctx)
	return subs, notShown(<-p, subs), <-b
}

// notShown is the plans not already on a subscription's card: a GLM
// Coding plan read with a Zhipu or Z.ai key is the ZCode account's own
// when ZCode is signed in to the same account, and shown once, on its
// card, which counts the calls. plans, PlanQuotas' cache, is left as it is.
func notShown(plans, subs []SubscriptionQuota) []SubscriptionQuota {
	return slices.DeleteFunc(slices.Clone(plans), func(p SubscriptionQuota) bool {
		return slices.ContainsFunc(subs, func(s SubscriptionQuota) bool { return sameAccount(p, s) })
	})
}

// sameAccount reports whether two cards are one account read twice: every
// window of the same length both say when they start again starts again
// at the same moment, and there is one such at least — a window's reset
// is when that account first used it.
func sameAccount(a, b SubscriptionQuota) bool {
	if a.Error != "" || b.Error != "" {
		return false
	}
	matched := false
	for _, x := range a.Windows {
		for _, y := range b.Windows {
			if x.Aside || y.Aside || x.Span == 0 || x.Span != y.Span || x.ResetsAt == nil || y.ResetsAt == nil {
				continue
			}
			if !x.ResetsAt.Equal(*y.ResetsAt) {
				return false
			}
			matched = true
		}
	}
	return matched
}

// Quota is one account's or key's allowance as magpie quota --json and
// the gateway's GET /v1/magpie/quotas tell it, for an agent choosing
// where to send its work: Provider is the first part of the models'
// ids (provider/model) for a key's plan or balance, the agent for a
// subscription.
type Quota struct {
	Provider string      `json:"provider"`
	Name     string      `json:"name"`
	Kind     string      `json:"kind"` // subscription, plan (bought with a key) or balance (a key's money)
	Plan     string      `json:"plan,omitempty"`
	User     string      `json:"user,omitempty"`
	Windows  []QuotaSpan `json:"windows"`
	Balance  string      `json:"balance,omitempty"`
	Error    string      `json:"error,omitempty"`
	AsOf     *time.Time  `json:"asOf,omitempty"` // the cached reading's time, nil for a new one
	// Until is when the plan's paid time ends, renewed then when Renew is
	// "auto", over when "off", either when "".
	Until *time.Time `json:"until,omitempty"`
	Renew string     `json:"renew,omitempty"`
	// Resets are a Codex account's rate-limit resets, when it holds any.
	Resets *ResetCredits `json:"resets,omitempty"`
}

// QuotaSpan is one window of an allowance: how much of it is used and
// left, in percent, and when it starts again.
type QuotaSpan struct {
	Unlimited bool       `json:"unlimited,omitempty"`
	Name      string     `json:"name"`
	Used      float64    `json:"used"`
	Remaining float64    `json:"remaining"`
	ResetsAt  *time.Time `json:"resetsAt,omitempty"`
	Display   string     `json:"display,omitempty"` // the vendor's own count, "1.2k / 3k"
}

// QuotaReport is Quotas as Quota, the reset times made absolute from now.
func QuotaReport(ctx context.Context, now time.Time) []Quota {
	subs, plans, balances := quotas(ctx)
	out := []Quota{}
	for _, g := range []struct {
		kind string
		qs   []SubscriptionQuota
	}{{"subscription", subs}, {"plan", plans}, {"balance", balances}} {
		for _, q := range g.qs {
			r := Quota{Provider: q.Provider, Name: q.Name, Kind: g.kind, Plan: q.Plan, User: q.User, AsOf: q.AsOf,
				Windows: []QuotaSpan{}, Balance: q.Balance, Error: q.Error, Until: q.Until, Renew: q.Renew, Resets: q.Resets}
			for _, w := range q.Windows {
				s := QuotaSpan{Unlimited: w.Unlimited, Name: w.Name, Used: w.Used, Remaining: max(0, 100-w.Used), ResetsAt: w.ResetsAt, Display: w.Display}
				if s.ResetsAt == nil && w.ResetSecs > 0 {
					t := now.Add(time.Duration(w.ResetSecs) * time.Second)
					s.ResetsAt = &t
				}
				r.Windows = append(r.Windows, s)
			}
			out = append(out, r)
		}
	}
	return out
}

// ResetClock is when a window starts again, on the clock: "14:30" today,
// "tomorrow 09:00", "Wed 14:30" within the week, else "Oct 3 14:30".
func ResetClock(at, now time.Time) string {
	at, now = at.Local(), now.Local()
	day := func(t time.Time) time.Time { y, m, d := t.Date(); return time.Date(y, m, d, 0, 0, 0, 0, time.Local) }
	switch days := int(math.Round(day(at).Sub(day(now)).Hours() / 24)); {
	case days <= 0:
		return at.Format("15:04")
	case days == 1:
		return "tomorrow " + at.Format("15:04")
	case days < 7:
		return at.Format("Mon 15:04")
	}
	return at.Format("Jan 2 15:04")
}

// PlanTerm says when a plan's paid time ends, "renews Oct 18", "expires
// Oct 18" or "until Oct 18" when the vendor doesn't say which; "" when
// it isn't known.
func PlanTerm(until *time.Time, renew string) string {
	if until == nil {
		return ""
	}
	d := until.Local().Format("Jan 2")
	switch renew {
	case "auto":
		return "renews " + d
	case "off":
		return "expires " + d
	}
	return "until " + d
}
