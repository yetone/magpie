package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Mistral's Le Chat plans (Pro, Student, Team) come with a month of Mistral
// Vibe, which the plan's Vibe key (chat.mistral.ai/code/extensions?focus=key)
// is told at the billing API on api.mistral.ai, as Mistral's own Vibe CLI
// reads it (mistralai/mistral-vibe, vibe/app_server/_billing.py and
// _account.py): GET /v1/billing/budget for the month's budget, and
// GET /v1/billing/subscription for the plan it is on (dhanyqaz on Discord,
// a Student plan). Le Chat's own message limits are told nowhere.
//
// The budget is the organization's, not the key's: every key of one
// organization is told the same vibe_budget and api_budget. Which of them a
// key spends is the key's kind, which console.mistral.ai's /api/vibe/whoami
// tells (plan_type "chat" for a Vibe key, "api" for a Studio key made in the
// console, "mistral_code"), as Vibe reads it (vibe/setup/auth/whoami.py) and
// reads the budget only of a chat key (_account.py). Reading vibe_budget for
// every key showed a Studio key the Vibe month (dhanyqaz on Discord: 149.36
// of 150 on both cards, and 298.72 of 300 for the two together).
const (
	mistralBudgetURL       = "https://api.mistral.ai/v1/billing/budget"
	mistralSubscriptionURL = "https://api.mistral.ai/v1/billing/subscription"
	mistralWhoamiURL       = "https://console.mistral.ai/api/vibe/whoami"
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
// "payg_enabled":false},"api_budget":{"usage_percentage":3.0,
// "initial_budget":50.0,"currency":"EUR","reset_at":"2026-11-01T00:00:00Z"}}
type mistralBudget struct {
	Vibe *mistralBudgetDetail `json:"vibe_budget"`
	API  *mistralBudgetDetail `json:"api_budget"`
}

type mistralBudgetDetail struct {
	UsagePercentage float64 `json:"usage_percentage"`
	InitialBudget   float64 `json:"initial_budget"`
	Currency        string  `json:"currency"`
	ResetAt         string  `json:"reset_at"`
	PaygEnabled     *bool   `json:"payg_enabled"`
}

// mistralSubscription: {"plan_name":"INDIVIDUAL","payg_enabled":true,
// "vibe_payg_enabled":true,"enterprise":false}
type mistralSubscription struct {
	PlanName        string `json:"plan_name"`
	PaygEnabled     *bool  `json:"payg_enabled"`
	VibePaygEnabled *bool  `json:"vibe_payg_enabled"`
}

// The kinds of key whoami tells, lower-cased as Vibe reads them.
const (
	mistralChatKey = "chat"
	mistralAPIKey  = "api"
)

// mistralKeyKinds: what whoami said of a key, by its hash; a key's kind
// doesn't change, so it is asked once.
var mistralKeyKinds sync.Map

// mistralKeyKind is the key's plan_type, asked of whoami; a reply that
// can't be read is an error, never a kind.
func mistralKeyKind(ctx context.Context, key string) (string, error) {
	h := sha256.Sum256([]byte(key))
	if k, ok := mistralKeyKinds.Load(h); ok {
		return k.(string), nil
	}
	b, err := planBody(ctx, planQuotaSource{url: mistralWhoamiURL, bearer: true}, key)
	if err != nil {
		return "", err
	}
	var r struct {
		PlanType *string `json:"plan_type"`
	}
	if err := json.Unmarshal(b, &r); err != nil || r.PlanType == nil {
		return "", fmt.Errorf("mistral whoami: no plan_type in its reply")
	}
	k := strings.ToLower(strings.TrimSpace(*r.PlanType))
	mistralKeyKinds.Store(h, k)
	return k, nil
}

// readMistralBudget is a Mistral key's month: what share of the budget
// its kind spends is used (vibe_budget for a Vibe key, api_budget for a
// Studio key), in the currency billing names, and when it resets. A
// budget billing doesn't name, or of 0, has no windows, and no card; a
// currency it doesn't name is left unsaid rather than guessed. With
// pay-as-you-go on (the budget's own, else payg, the subscription's),
// what goes past the budget is billed rather than stopped, so the window
// doesn't stop the models. The Studio month is "API · Month", so the
// accounts' strip never adds it to a Vibe month.
func readMistralBudget(b []byte, kind string, payg *bool) ([]QuotaWindow, error) {
	var r mistralBudget
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	v, name := r.Vibe, "Month"
	if kind == mistralAPIKey {
		v, name = r.API, "API · Month"
	} else if kind != mistralChatKey {
		v = nil
	}
	if v == nil || v.InitialBudget <= 0 {
		return []QuotaWindow{}, nil
	}
	used := max(0, v.UsagePercentage)
	w := QuotaWindow{Name: name, Used: min(100, used), Limit: v.InitialBudget,
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

// readMistralPlan is the source's reader without a key to ask whoami
// with: it tells no window, since which budget a key spends is the key's.
func readMistralPlan(b []byte) (string, []QuotaWindow, error) {
	ws, err := readMistralBudget(b, "", nil)
	return "", ws, err
}

// mistralKeyWindows is readMistralBudget's window for the key's kind, with
// the plan it is on, asked only of a key that has a budget; the card
// stands without its name when billing/subscription can't be read. A key
// whose kind whoami doesn't tell has no card read: the organization's
// budget alone doesn't say which of its two it spends.
func mistralKeyWindows(ctx context.Context, src planQuotaSource, key string) (string, []QuotaWindow, error) {
	kind, err := mistralKeyKind(ctx, key)
	if err != nil {
		return "", nil, err
	}
	if kind != mistralChatKey && kind != mistralAPIKey {
		return "", []QuotaWindow{}, nil
	}
	b, err := planBody(ctx, src, key)
	if err != nil {
		return "", nil, err
	}
	ws, err := readMistralBudget(b, kind, nil)
	if err != nil || len(ws) == 0 {
		return "", ws, err
	}
	var sub mistralSubscription
	if sb, err := planBody(ctx, planQuotaSource{url: mistralSubscriptionURL, bearer: true}, key); err == nil && json.Unmarshal(sb, &sub) == nil {
		payg := sub.VibePaygEnabled
		if kind == mistralAPIKey {
			payg = sub.PaygEnabled
		}
		ws, _ = readMistralBudget(b, kind, payg)
	}
	name := strings.ToUpper(strings.TrimSpace(sub.PlanName))
	plan := mistralPlanTitles[name]
	if plan == "" {
		plan = strings.TrimSpace(sub.PlanName)
	}
	return plan, ws, nil
}
