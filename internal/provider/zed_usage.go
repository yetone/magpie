package provider

// PLUGIN-SERVED (see AGENTS.md): Zed ("zed") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-zed-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/zed) and raise the mover's
// min in internal/provider/migrate_zed.go.

// What a Zed account says of its plan. Zed tells its editor the plan and its
// billing period, not how much of the model allowance is spent, so that is
// all magpie can show: the plan's name, when the period ends, and a plan
// Zed won't serve (overdue invoices) as the error it is.

import (
	"context"
	"time"

	"github.com/yetone/magpie/internal/zed"
)

func zedLoginQuota(ctx context.Context, l Login) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "zed", Name: "Zed", Icon: "zed", Plan: l.Plan, User: l.User, Windows: []QuotaWindow{}}
	c, ok := zedLookup(l.User)
	if !ok {
		q.Error = "no such Zed account"
		return q
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	me, err := zed.FetchMe(ctx, zedClient, zedCloud, c.UserID, c.Access, c.SystemID)
	if err != nil {
		if zedRefused(err) {
			err = zedLapse(l.User)
		}
		q.Error = err.Error()
		return q
	}
	org := c.Org
	if org == "" {
		org = me.Org()
	}
	plan := me.PlanID(org)
	q.Plan = zed.PlanName(plan)
	if plan != c.Plan {
		_ = zedEdit(l.User, func(c *zedCreds, s *savedLogin) { c.Plan, s.Plan = plan, q.Plan })
	}
	if p := me.Plan.SubscriptionPeriod; p != nil {
		if at, err := time.Parse(time.RFC3339, p.EndedAt); err == nil {
			q.Until = &at
		}
	}
	switch {
	case me.Plan.HasOverdueInvoice:
		q.Error = "Zed: this account has an overdue invoice, so its models are paused (see zed.dev/account)"
	case plan == "zed_free" || plan == "":
		q.Plan = "No plan"
	}
	return q
}
