package provider

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// A window's runsOutAt reached QuotaWindow, and the card /v1/magpie/quotas
// and magpie quota --json tell is made of QuotaSpan: quotaReport built each
// span field by field without it, so a WorkBuddy account's credits, which
// end package by package (a free account's with no reset at all), said
// nothing there of when they go (#3762, MYCF711).
func TestCreditsRunningOutReachTheQuotaReport(t *testing.T) {
	now := time.Date(2026, 10, 26, 22, 0, 0, 0, time.UTC)
	at := time.Date(2026, 10, 30, 17, 34, 42, 0, time.UTC)
	// the plugin's window as workbuddy 0.1.35 answers for a free account:
	// no resetsAt, the soonest package's end in runsOutAt
	used := 100 * 282 / 3400.0
	u := plugin.Usage{Plan: "Free plan", Windows: []plugin.UsageWindow{{Name: "Credits", Used: &used,
		Display: "282 / 3400", Amount: 282, Limit: 3400, Unit: "credits", RunsOutAt: "2026-10-30T17:34:42.000Z", RunsOut: 18.44}}}
	q := quotaOfPlugin(SubscriptionQuota{Kind: "subscription", Provider: "workbuddy", User: "133****0660"}, u)

	got := quotaReport([]SubscriptionQuota{q}, nil, nil, now)
	if len(got) != 1 || len(got[0].Windows) != 1 {
		t.Fatalf("cards: %+v", got)
	}
	b, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Windows []map[string]any `json:"windows"`
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	w := back.Windows[0]
	if w["runsOutAt"] != at.Format(time.RFC3339) || w["runsOut"] != 18.44 {
		t.Fatalf("the window's end isn't told: %s", b)
	}
	// a window with no reset says none, rather than taking the end for one
	if _, ok := w["resetsAt"]; ok {
		t.Fatalf("resetsAt made up: %s", b)
	}
}

// Every field of a window that is sent as JSON is carried onto the span the
// report tells, or is named here as left out on purpose: a field added to
// QuotaWindow can't again be dropped on the way to /v1/magpie/quotas
// without anyone deciding it (#3762 was RunsOutAt).
func TestQuotaSpanCarriesEveryWindowField(t *testing.T) {
	leftOut := map[string]string{
		"resetSecs": "made into resetsAt from the report's now",
		"family":    "the Usage page's grouping of per-model windows",
		"credits":   "routing's (Allowance.OnCredits)",
		"capped":    "the GUI's cap marks (WithCapped)",
		"capsSome":  "the GUI's cap marks (WithCapped)",
		"capId":     "the GUI's cap marks (WithCapped)",
		"ownCap":    "the GUI's cap marks (WithCapped)",
		"startsAt":  "the GUI's Pace number (WithWindowStarts)",
		"trayKind":  "the GUI's menu bar picker (gui.withTrayKinds)",
		"trayExtra": "the GUI's menu bar picker (gui.withTrayKinds)",
		"heldBy":    "the GUI's all-accounts sum (WithHeldWindows)",
	}
	names := func(t reflect.Type) map[string]bool {
		out := map[string]bool{}
		for i := range t.NumField() {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if f.IsExported() && name != "" && name != "-" {
				out[name] = true
			}
		}
		return out
	}
	span := names(reflect.TypeFor[QuotaSpan]())
	for name := range names(reflect.TypeFor[QuotaWindow]()) {
		if !span[name] && leftOut[name] == "" {
			t.Errorf("QuotaWindow's %q isn't on QuotaSpan: carry it in quotaReport, or name it as left out here", name)
		}
	}

	// and what is carried is copied: a window with each set says each
	at := time.Date(2026, 10, 30, 17, 0, 0, 0, time.UTC)
	bal := 6291.0
	full := QuotaWindow{Balance: &bal, Unlimited: true, Name: "Credits", Used: 40, ResetsAt: &at, Display: "1 / 2", Text: true, Amount: 1, Limit: 2, Unit: "credits",
		RunsOutAt: &at, RunsOut: 3, Pool: "P", Holds: &WindowHolds{}, HoldsWhy: "x", Spent: &bal, SpentSince: &at}
	got := quotaReport([]SubscriptionQuota{{Provider: "p", Windows: []QuotaWindow{full}}}, nil, nil, at)
	b, _ := json.Marshal(got[0].Windows[0])
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for name := range span {
		if _, ok := m[name]; !ok {
			t.Errorf("%q is on QuotaSpan but quotaReport leaves it empty: %s", name, b)
		}
	}
}
