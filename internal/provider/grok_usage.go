package provider

// PLUGIN-SERVED (see AGENTS.md): Grok ("grok") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-grok-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/grok) and raise the mover's
// min in internal/provider/migrate_side.go.

// How much of a Grok subscription's allowance is gone, as the CLI's own
// /usage reads it: the credits of the current period, weekly for SuperGrok,
// and what on-demand spending has used of its cap.

import (
	"context"
	"strings"
	"time"
)

// GrokBase is the CLI's backend; a var so tests can point it elsewhere.
var GrokBase = "https://cli-chat-proxy.grok.com/v1"

func grokSubscriptionUsage(ctx context.Context) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "grok", Name: "Grok (SuperGrok)", Icon: "xai", Windows: []QuotaWindow{}}
	if c, ok := readGrokCredential(GrokHome()); ok {
		q.User = c.Email
	}
	c, err := grokAccessToken(GrokHome(), GrokExecutable(), false)
	if err == nil {
		q.Windows, err = grokWindows(ctx, c.Key)
	}
	if err != nil {
		q.Error = err.Error()
	}
	return q
}

type grokAmount struct {
	Val float64 `json:"val"`
}

// grokWindows: the credits used this period, and the on-demand spending
// when the account allows any.
func grokWindows(ctx context.Context, token string) ([]QuotaWindow, error) {
	var data struct {
		Config struct {
			CreditUsagePercent float64 `json:"creditUsagePercent"`
			CurrentPeriod      *struct {
				Type string `json:"type"`
				End  string `json:"end"`
			} `json:"currentPeriod"`
			BillingPeriodEnd string     `json:"billingPeriodEnd"`
			OnDemandCap      grokAmount `json:"onDemandCap"`
			OnDemandUsed     grokAmount `json:"onDemandUsed"`
		} `json:"config"`
	}
	if err := accountJSON(ctx, GrokBase+"/billing?format=credits", token, nil, &data); err != nil {
		return []QuotaWindow{}, err
	}
	cfg := data.Config
	name, end := "Allowance", cfg.BillingPeriodEnd
	if p := cfg.CurrentPeriod; p != nil {
		name, end = grokPeriodName(p.Type), p.End
	}
	w := QuotaWindow{Name: name, Used: cfg.CreditUsagePercent, Span: grokPeriodSpan[name]}
	if t, err := time.Parse(time.RFC3339Nano, end); err == nil {
		w.ResetsAt = &t
	}
	out := []QuotaWindow{w}
	if cfg.OnDemandCap.Val > 0 {
		out = append(out, QuotaWindow{Name: "On-demand", Used: 100 * cfg.OnDemandUsed.Val / cfg.OnDemandCap.Val, ResetsAt: w.ResetsAt, Aside: true})
	}
	return out, nil
}

// grokPeriodName names a USAGE_PERIOD_TYPE_* the way the other allowances
// are named.
func grokPeriodName(t string) string {
	switch strings.TrimPrefix(t, "USAGE_PERIOD_TYPE_") {
	case "DAILY":
		return "1 day"
	case "WEEKLY":
		return "7 days"
	case "MONTHLY":
		return "Month"
	}
	return "Allowance"
}

// grokPeriodSpan is how long each of grokPeriodName's periods runs.
var grokPeriodSpan = map[string]time.Duration{"1 day": 24 * time.Hour, "7 days": 7 * 24 * time.Hour, "Month": 30 * 24 * time.Hour}
