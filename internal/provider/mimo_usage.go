package provider

// PLUGIN-SERVED (see AGENTS.md): Xiaomi MiMo ("mimo-app") is a deprecated
// built-in subscription served by its plugin,
// @magpie-community/opencode-mimo-auth, once moved onto it (provider.Moved;
// the default for a new sign-in). A moved one's sign-ins, models, requests
// and usage are all the plugin's, never this code's (only the move, in
// migrate*.go, still reads its accounts). A fix here alone doesn't reach
// those users; fix the plugin (github.com/magpie-community/plugins,
// packages/mimo) and raise the mover's min in
// internal/provider/migrate_mimo.go.

// What a MiMo account says of its allowance, from the pages the app's
// "Usage & billing" reads: /user/usage, how much of the week's allowance
// is left (percent remaining, and the day it resets; no reset date is no
// plan), and /user/xiaomi/subscription/self, the plan in force and when
// it ends. An account with no plan is on MiMo's free offer.

import (
	"context"
	"errors"
	"strings"
	"time"
)

// mimoTiers are the app's names for its plans' tiers.
var mimoTiers = map[int]string{1: "Starter", 2: "Plus", 3: "Pro", 4: "Ultra"}

// mimoZone is the MiMo server's clock: its times carry no zone, and the
// app shows them as they come, which is Beijing's (and Singapore's) time.
var mimoZone = time.FixedZone("UTC+8", 8*60*60)

type mimoSub struct {
	PlanCode    string  `json:"planCode"`
	Title       string  `json:"title"`
	PlanTier    int     `json:"planTier"`
	Status      string  `json:"status"`
	RenewalMode string  `json:"renewalMode"` // MONTHLY, YEARLY, ONE_TIME
	EndTime     string  `json:"endTime"`
	Percent     float64 `json:"percent"`
	NextReset   string  `json:"nextResetTime"`
	Source      string  `json:"source"` // ORDER_SUB, INVITE
}

// mimoTime reads one of the server's times: "2026-10-01T00:00:00", or a
// day alone.
func mimoTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, mimoZone); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// mimoPlan is the plan an account is on: its name ("Free" with none) and
// what the server says of it (nil with none).
func mimoPlan(ctx context.Context, user string) (string, *mimoSub, error) {
	var self struct {
		Current *mimoSub `json:"current"`
	}
	if err := mimoGet(ctx, user, "/user/xiaomi/subscription/self", &self); err != nil {
		return "", nil, err
	}
	c := self.Current
	if c == nil {
		return "Free", nil, nil
	}
	return firstNonEmpty(c.Title, mimoTiers[c.PlanTier], c.PlanCode, "MiMo"), c, nil
}

func mimoLoginQuota(ctx context.Context, l Login) SubscriptionQuota {
	q := SubscriptionQuota{Provider: MiMoID, Name: "Xiaomi MiMo", Icon: "mimocode", Plan: l.Plan, User: l.User, Windows: []QuotaWindow{}}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	plan, sub, err := mimoPlan(ctx, l.User)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	q.Plan = plan
	if plan != l.Plan {
		_ = mimoEdit(l.User, func(_ *mimoCreds, s *savedLogin) { s.Plan = plan })
	}
	if sub != nil {
		if at, ok := mimoTime(sub.EndTime); ok {
			q.Until = &at
		}
		switch sub.RenewalMode {
		case "MONTHLY", "YEARLY":
			q.Renew = "auto"
		case "ONE_TIME":
			q.Renew = "off"
		}
	}
	var use struct {
		Percent   *float64 `json:"percent"`
		ResetDate *string  `json:"resetDate"`
	}
	if err := mimoGet(ctx, l.User, "/user/usage", &use); err != nil {
		if errors.Is(err, ErrMiMoSignIn) {
			q.Error = err.Error()
		}
		// the plan alone, when the week's allowance can't be read
		return q
	}
	if use.Percent == nil || use.ResetDate == nil {
		return q // no plan: the free offer shows no allowance
	}
	w := QuotaWindow{Name: "7 days", Used: max(0, min(100, 100-*use.Percent)), Span: 7 * 24 * time.Hour}
	if at, ok := mimoTime(*use.ResetDate); ok {
		w.ResetsAt = &at
	}
	q.Windows = append(q.Windows, w)
	return q
}
