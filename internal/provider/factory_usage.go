package provider

// PLUGIN-SERVED (see AGENTS.md): Factory ("factory") is a deprecated
// built-in subscription served by its plugin,
// @magpie-community/opencode-factory-auth, once moved onto it
// (provider.Moved; the default for a new sign-in). A moved one's sign-ins,
// models, requests and usage are all the plugin's, never this code's (only
// the move, in migrate*.go, still reads its accounts). A fix here alone
// doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/factory) and raise the
// mover's min in internal/provider/migrate_factory.go.

// How much of a Factory plan an account has used, as droid's /status asks
// it: GET /api/billing/limits. Standard usage runs in rolling 5-hour, weekly
// and monthly windows, each a percent used and when it ends; Droid Core, the
// open models Factory hosts, has windows of its own; extra usage is a
// balance in cents.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

type factoryWindow struct {
	Used float64         `json:"usedPercent"`
	End  json.RawMessage `json:"windowEnd"`
}

type factoryPool struct {
	FiveHour *factoryWindow `json:"fiveHour"`
	Weekly   *factoryWindow `json:"weekly"`
	Monthly  *factoryWindow `json:"monthly"`
}

type factoryLimits struct {
	Limits struct {
		Standard *factoryPool `json:"standard"`
		Core     *factoryPool `json:"core"`
	} `json:"limits"`
	Extra        *float64 `json:"extraUsageBalanceCents"`
	ExtraAllowed bool     `json:"extraUsageAllowed"`
}

// factoryWhen reads a window's end: an ISO time or epoch milliseconds, as
// droid hands either to new Date.
func factoryWhen(raw json.RawMessage) *time.Time {
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return &t
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			t := time.UnixMilli(n).UTC()
			return &t
		}
		return nil
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil && n > 0 {
		t := time.UnixMilli(int64(n)).UTC()
		return &t
	}
	return nil
}

// factoryQuota turns the limits into windows: standard's by their span,
// then Droid Core's, then the extra usage left. For routing, standard counts
// the vendors' models and Core the open ones Factory hosts; with extra usage
// to spend, a window used up stops neither.
func factoryQuota(l factoryLimits) []QuotaWindow {
	var out []QuotaWindow
	extra := l.ExtraAllowed && l.Extra != nil && *l.Extra > 0
	add := func(p *factoryPool, prefix string, core bool) {
		if p == nil {
			return
		}
		for _, w := range []struct {
			win  *factoryWindow
			name string
			span time.Duration
		}{
			{p.FiveHour, "5 hours", 5 * time.Hour},
			{p.Weekly, "7 days", 7 * 24 * time.Hour},
			{p.Monthly, "30 days", 30 * 24 * time.Hour},
		} {
			if w.win == nil {
				continue
			}
			out = append(out, QuotaWindow{Name: prefix + w.name, Used: min(max(w.win.Used, 0), 100),
				Span: w.span, ResetsAt: factoryWhen(w.win.End), Aside: extra,
				matches: func(model string) bool { return factoryCore(model) == core }})
		}
	}
	add(l.Limits.Standard, "", false)
	add(l.Limits.Core, "Droid Core · ", true)
	if l.Extra != nil && *l.Extra > 0 {
		out = append(out, QuotaWindow{Name: "Extra usage", Display: fmt.Sprintf("$%.2f", *l.Extra/100), Aside: true})
	}
	return out
}

func factoryLoginQuota(ctx context.Context, l Login) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "factory", Name: "Factory", Icon: "factory",
		Plan: l.Plan, User: l.User, Windows: []QuotaWindow{}}
	c, err := factoryFresh(ctx, l.User)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var lim factoryLimits
	err = factoryGet(ctx, c, "/api/billing/limits", nil, &lim)
	var st *factoryStatus
	if errors.As(err, &st) && factoryMendOrg(ctx, l.User, st.Code, []byte(st.Msg)) {
		// the org it named was put right: ask once more
		if c, err = factoryFresh(ctx, l.User); err == nil {
			err = factoryGet(ctx, c, "/api/billing/limits", nil, &lim)
		}
	}
	if err != nil {
		q.Error = err.Error()
		return q
	}
	if lim.Limits.Standard == nil {
		q.Error = "Factory: the account reported no limits"
		return q
	}
	q.Windows = factoryQuota(lim)
	return q
}
