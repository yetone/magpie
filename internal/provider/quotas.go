package provider

import (
	"context"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Quotas is what is left everywhere magpie can ask: the signed-in
// subscriptions' windows, the plans bought with a key, and the keys'
// balances, as the Usage page shows them. The vendors are asked at once;
// what doesn't answer before ctx ends comes back with an error.
func Quotas(ctx context.Context) []SubscriptionQuota {
	subs, plans, balances := quotas(ctx)
	return cardsServed(subs, plans, balances, LastServed())
}

// InProviderOrder is qs in the order the user put the providers in on the
// Providers page (SetOrder), each provider's cards as they came; a card of
// no provider here (a remote magpie's) after them all. The window's Usage
// page and the tray panel's Allowances tab draw them so, where the user
// hasn't arranged the cards themselves (Zhenbao TONG on Discord: the
// providers moved in the window, the tray kept its own order).
func InProviderOrder(qs []SubscriptionQuota) []SubscriptionQuota {
	at := map[string]int{}
	for i, p := range All() {
		at[p.ID] = i
	}
	rank := func(q SubscriptionQuota) int {
		if i, ok := at[q.Provider]; ok {
			return i
		}
		return len(at)
	}
	out := slices.Clone(qs)
	slices.SortStableFunc(out, func(a, b SubscriptionQuota) int { return rank(a) - rank(b) })
	return out
}

// Allotted is Quotas without the keys' balances: the accounts and plans
// whose allowance runs out and starts again, which is what magpie quota
// wait waits on; no key's balance is asked for.
func Allotted(ctx context.Context) []SubscriptionQuota {
	plans, subs := seatCards(PlanQuotas(ctx), SubscriptionUsage(ctx))
	return append(withAccountNames(subs), plans...)
}

func quotas(ctx context.Context) (subs, plans, balances []SubscriptionQuota) {
	b := make(chan []SubscriptionQuota, 1)
	p := make(chan []SubscriptionQuota, 1)
	r := make(chan []SubscriptionQuota, 1)
	go func() { b <- KeyBalances(ctx) }()
	go func() { p <- PlanQuotas(ctx) }()
	go func() { r <- RemoteCards(ctx) }()
	plans, subs = seatCards(<-p, SubscriptionUsage(ctx))
	subs, balances = WithImageLane(withAccountNames(subs), time.Now()), <-b
	// a remote magpie's cards, each after this computer's of its kind
	for _, q := range <-r {
		switch q.Kind {
		case "plan":
			plans = append(plans, q)
		case "balance":
			balances = append(balances, q)
		default:
			subs = append(subs, q)
		}
	}
	return subs, plans, balances
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
	// BalanceSplit is what the Balance is made of, when the vendor tells
	// it (SubscriptionQuota's)
	BalanceSplit []BalancePart `json:"balanceSplit,omitempty"`
	// SharedBalance is the first of the keys whose Balance is this key's
	// account's too (SubscriptionQuota's)
	SharedBalance string     `json:"sharedBalance,omitempty"`
	Error         string     `json:"error,omitempty"`
	AsOf          *time.Time `json:"asOf,omitempty"` // the cached reading's time, nil for a new one
	// ReadDeferred says no request was made this time, rather than one failed.
	ReadDeferred bool `json:"readDeferred,omitempty"`
	// ReadAt is when the windows or balance shown were read, nil when not
	// known; a Claude account's can be well before now (claudeReadAt).
	ReadAt *time.Time `json:"readAt,omitempty"`
	// Until is when the plan's paid time ends, renewed then when Renew is
	// "auto", over when "off", either when "".
	Until *time.Time `json:"until,omitempty"`
	Renew string     `json:"renew,omitempty"`
	// Resets are a Codex account's rate-limit resets, when it holds any.
	Resets *ResetCredits `json:"resets,omitempty"`
	// ResetsUsed are the Codex account's resets magpie spent, newest
	// first, by itself or by hand (codex_resets_used.go)
	ResetsUsed []ResetUse `json:"resetsUsed,omitempty"`
	// LastServedAt is when the account, plan or key last answered a
	// request through the gateway, nil when it hasn't in the last 30 days
	// (served.go); Last is on the latest of them (#570).
	LastServedAt *time.Time `json:"lastServedAt,omitempty"`
	Last         bool       `json:"last,omitempty"`
	// From is the remote magpie whose account this is, by its name here
	// (remote_quotas.go); "" for this computer's own.
	From string `json:"from,omitempty"`
	// Alias is the account's name of the user's own, else Seat: the name
	// of the key plan whose card this account's stands in for (#1515).
	Alias string `json:"alias,omitempty"`
	Seat  string `json:"seat,omitempty"`
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
	// Text: the window is its Display alone, Used and Remaining no
	// reading (QuotaWindow.Text)
	Text bool `json:"text,omitempty"`
	// Lane names an allowance apart from the account's own (QuotaWindow.Lane)
	Lane string `json:"lane,omitempty"`
	// Amount of Limit in Unit: the window's count, used, when the vendor
	// counts it so ("credits")
	Amount float64 `json:"amount,omitempty"`
	Limit  float64 `json:"limit,omitempty"`
	Unit   string  `json:"unit,omitempty"`
	Pool   string  `json:"pool,omitempty"` // the shared pool the window draws on ("Gemini")
	// RunsOutAt is when the soonest of what is left is lost, apart from
	// the reset, and RunsOut how much (QuotaWindow.RunsOutAt): WorkBuddy's
	// credits come in packages that each end on their own day, and a free
	// account's have no reset at all (#3762).
	RunsOutAt *time.Time `json:"runsOutAt,omitempty"`
	RunsOut   float64    `json:"runsOut,omitempty"`
	// Balance is how much is left in Unit, a prepaid wallet's reading
	// (QuotaWindow.Balance, #3950): Used and Remaining are then no share.
	Balance *float64 `json:"balance,omitempty"`
	// Spent is what magpie counted such a wallet spending since
	// SpentSince, its period's first reading (QuotaWindow.Spent, #5770).
	Spent      *float64   `json:"spent,omitempty"`
	SpentSince *time.Time `json:"spentSince,omitempty"`
	// Holds and HoldsWhy are the window's whole as the Usage page's card
	// tells it (QuotaWindow.Holds, #2333): reckoned when ReckonHolds is
	// set, else left out.
	Holds    *WindowHolds `json:"holds,omitempty"`
	HoldsWhy string       `json:"holdsWhy,omitempty"`
}

// ReckonHolds is usage.WithWindowHolds, which package usage sets: it reads
// the usage log, which this package can't import. QuotaReport,
// KeptQuotaReport and RefreshReport tell each window's whole by it, as the
// Usage page does (#2333); nil, where usage isn't linked in, leaves it out.
var ReckonHolds func(qs []SubscriptionQuota, now time.Time) []SubscriptionQuota

// QuotaReport is Quotas as Quota, the reset times made absolute from now.
func QuotaReport(ctx context.Context, now time.Time) []Quota {
	subs, plans, balances := quotas(ctx)
	return withServed(quotaReport(subs, plans, balances, now), LastServed())
}

// KeptQuotaReport is QuotaReport from what was last read (CachedCards),
// asking no vendor: for a status line, which Claude Code runs again on
// every message (#1997), so it must neither wait for a read nor start one.
func KeptQuotaReport(now time.Time) []Quota {
	var subs, plans, balances []SubscriptionQuota
	for _, q := range CachedCards(now) {
		switch q.Kind {
		case "plan":
			plans = append(plans, q)
		case "balance":
			balances = append(balances, q)
		default:
			subs = append(subs, q)
		}
	}
	return withServed(quotaReport(subs, plans, balances, now), LastServed())
}

// quotaReport assembles the cards from readings the tests feed directly.
func quotaReport(subs, plans, balances []SubscriptionQuota, now time.Time) []Quota {
	if ReckonHolds != nil { // one pass over the log for every card
		all := ReckonHolds(slices.Concat(subs, plans, balances), now)
		ns, np := len(subs), len(plans)
		subs, plans, balances = all[:ns], all[ns:ns+np], all[ns+np:]
	}
	out := []Quota{}
	for _, g := range []struct {
		kind string
		qs   []SubscriptionQuota
	}{{"subscription", subs}, {"plan", plans}, {"balance", balances}} {
		for _, q := range g.qs {
			r := Quota{Provider: q.Provider, Name: q.Name, Kind: g.kind, Plan: q.Plan, User: q.User, AsOf: q.AsOf, ReadAt: q.ReadAt, ReadDeferred: q.ReadDeferred,
				Windows: []QuotaSpan{}, Balance: q.Balance, BalanceSplit: q.BalanceSplit, SharedBalance: q.SharedBalance, Error: q.Error, Until: q.Until, Renew: q.Renew, Resets: q.Resets, ResetsUsed: q.ResetsUsed, From: q.From, Alias: q.Alias, Seat: q.Seat}
			// a pool's own windows stand in for the models' drawing on it,
			// as the usage page shows them
			for _, w := range PooledWindows(q.Windows) {
				s := QuotaSpan{Unlimited: w.Unlimited, Name: w.Name, Used: w.Used, Remaining: max(0, 100-w.Used), ResetsAt: w.ResetsAt, Display: w.Display, Text: w.Text, Lane: w.Lane,
					Amount: w.Amount, Limit: w.Limit, Unit: w.Unit, Pool: w.Pool, RunsOutAt: w.RunsOutAt, RunsOut: w.RunsOut, Balance: w.Balance,
					Spent: w.Spent, SpentSince: w.SpentSince, Holds: w.Holds, HoldsWhy: w.HoldsWhy}
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

// BalanceText is a wallet's balance as told, the figure in its unit:
// "6,291 points left" (QuotaWindow.Balance).
func BalanceText(b float64, unit string) string {
	s := strconv.FormatFloat(math.Round(b*100)/100, 'f', -1, 64)
	whole, frac, _ := strings.Cut(strings.TrimPrefix(s, "-"), ".")
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	if frac != "" {
		whole += "." + frac
	}
	if b < 0 {
		whole = "-" + whole
	}
	return strings.TrimSpace(whole+" "+unit) + " left"
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
