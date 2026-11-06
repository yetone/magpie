package provider

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Mistral's Le Chat plans (Pro, Student, Team) come with a month of Mistral
// Vibe, which the plan's Vibe key (chat.mistral.ai/code/extensions?focus=key)
// is told at the billing API on api.mistral.ai, as Mistral's own Vibe CLI
// reads it (mistralai/mistral-vibe, vibe/app_server/_billing.py and
// _account.py): GET /v1/billing/budget for the month's budget, and
// GET /v1/billing/subscription for the plan it is on (dhanyqaz on Discord,
// a Student plan). Le Chat's own message limits are told nowhere.
const (
	mistralBudgetURL       = "https://api.mistral.ai/v1/billing/budget"
	mistralSubscriptionURL = "https://api.mistral.ai/v1/billing/subscription"
)

// mistralPlanTitles are the plans billing names, as Vibe titles them.
var mistralPlanTitles = map[string]string{
	"FREE":       "Free",
	"INDIVIDUAL": "Pro",
	"EDU":        "Student",
	"TEAM":       "Team",
}

// mistralBudget: {"vibe_budget":{"usage_percentage":42.5,
// "initial_budget":300.0,"currency":"EUR","reset_at":"2026-11-01T00:00:00Z",
// "payg_enabled":false},"api_budget":{…}}
type mistralBudget struct {
	Vibe *struct {
		UsagePercentage float64 `json:"usage_percentage"`
		InitialBudget   float64 `json:"initial_budget"`
		Currency        string  `json:"currency"`
		ResetAt         string  `json:"reset_at"`
		PaygEnabled     *bool   `json:"payg_enabled"`
	} `json:"vibe_budget"`
}

// mistralSubscription: {"plan_name":"INDIVIDUAL","payg_enabled":true,
// "vibe_payg_enabled":true,"enterprise":false}
type mistralSubscription struct {
	PlanName        string `json:"plan_name"`
	VibePaygEnabled *bool  `json:"vibe_payg_enabled"`
}

// readMistralBudget is a Mistral key's Vibe month: what share of the
// budget is used, in the budget's currency, and when it resets. A key with
// no Vibe budget (a la Plateforme key, or none in its plan) has no
// windows, and no card. With pay-as-you-go on (the budget's own, else
// payg, the subscription's), what goes past the budget is billed rather
// than stopped, so the window doesn't stop the models.
func readMistralBudget(b []byte, payg *bool) ([]QuotaWindow, error) {
	var r mistralBudget
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	v := r.Vibe
	if v == nil || v.InitialBudget <= 0 {
		return []QuotaWindow{}, nil
	}
	used := max(0, v.UsagePercentage)
	w := QuotaWindow{Name: "Month", Used: min(100, used), Limit: v.InitialBudget,
		Amount: v.InitialBudget * used / 100, Unit: strings.ToUpper(strings.TrimSpace(v.Currency))}
	if t, err := time.Parse(time.RFC3339, v.ResetAt); err == nil {
		w.ResetsAt = &t
		w.Span = t.Sub(t.AddDate(0, -1, 0))
	}
	if v.PaygEnabled != nil {
		payg = v.PaygEnabled
	}
	w.Aside = payg != nil && *payg
	return []QuotaWindow{w}, nil
}

func readMistralPlan(b []byte) (string, []QuotaWindow, error) {
	ws, err := readMistralBudget(b, nil)
	return "", ws, err
}

// mistralKeyWindows is readMistralBudget's window with the plan it is on,
// asked only of a key that has a budget; the card stands without its
// name when billing/subscription can't be read.
func mistralKeyWindows(ctx context.Context, src planQuotaSource, key string) (string, []QuotaWindow, error) {
	b, err := planBody(ctx, src, key)
	if err != nil {
		return "", nil, err
	}
	ws, err := readMistralBudget(b, nil)
	if err != nil || len(ws) == 0 {
		return "", ws, err
	}
	var sub mistralSubscription
	if sb, err := planBody(ctx, planQuotaSource{url: mistralSubscriptionURL, bearer: true}, key); err == nil && json.Unmarshal(sb, &sub) == nil {
		ws, _ = readMistralBudget(b, sub.VibePaygEnabled)
	}
	name := strings.ToUpper(strings.TrimSpace(sub.PlanName))
	plan := mistralPlanTitles[name]
	if plan == "" {
		plan = strings.TrimSpace(sub.PlanName)
	}
	return plan, ws, nil
}
