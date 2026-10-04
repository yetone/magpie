package provider

import (
	"strings"
	"testing"
	"time"
)

// A Zhipu plan read with a key of the account ZCode is signed in to is
// shown once, on ZCode's card; another account's plan stays.
func TestPlanOfASignedInAccountShownOnce(t *testing.T) {
	at := func(h int) *time.Time {
		x := time.UnixMilli(1790000000000).Add(time.Duration(h) * time.Hour)
		return &x
	}
	five, week := 5*time.Hour, 7*24*time.Hour
	zcode := SubscriptionQuota{Provider: "zcode", Windows: []QuotaWindow{
		{Name: "5 hours", Span: five, Used: 1, ResetsAt: at(2)},
		{Name: "Weekly", Span: week, Used: 32, ResetsAt: at(90)},
	}}
	same := SubscriptionQuota{Provider: "zhipu", Plan: "Max", Windows: []QuotaWindow{
		{Name: "5 hours", Span: five, Used: 4, ResetsAt: at(2)},
		{Name: "7 days", Span: week, Used: 32, ResetsAt: at(90)},
		{Name: "MCP · Month", Aside: true, ResetsAt: at(300)},
	}}
	other := SubscriptionQuota{Provider: "zai", Plan: "Pro", Windows: []QuotaWindow{
		{Name: "5 hours", Span: five, ResetsAt: at(3)},
		{Name: "7 days", Span: week, ResetsAt: at(90)},
	}}
	unknown := SubscriptionQuota{Provider: "zai", Plan: "Lite", Windows: []QuotaWindow{{Name: "5 hours", Span: five}}}
	got := notShown([]SubscriptionQuota{same, other, unknown}, []SubscriptionQuota{zcode})
	if len(got) != 2 || got[0].Provider != "zai" || got[1].Plan != "Lite" {
		t.Fatalf("%+v", got)
	}
	// ZCode not answering: the plan is the only card there is
	zcode.Error = "timeout"
	if got := notShown([]SubscriptionQuota{same}, []SubscriptionQuota{zcode}); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}

// Leaving a plan out doesn't touch the list it was left out of, which is
// PlanQuotas' cache.
func TestPlanLeftOutOfACopy(t *testing.T) {
	r := time.Now()
	w := []QuotaWindow{{Span: time.Hour, ResetsAt: &r}}
	plans := []SubscriptionQuota{{Provider: "a", Windows: w}, {Provider: "b"}}
	notShown(plans, []SubscriptionQuota{{Windows: w}})
	if plans[0].Provider != "a" || plans[1].Provider != "b" {
		t.Fatalf("%+v", plans)
	}
}

// magpie quota, its --json and the gateway show a pool's own windows in
// place of the models' drawing on it (the shapes TestAntigravityQuotaPools
// records): one span per pool window, the pool in its name.
func TestQuotaReportPoolsAntigravity(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	reset := now.Add(48 * time.Hour)
	five, week := 5*time.Hour, 7*24*time.Hour
	antigravity := SubscriptionQuota{Provider: "antigravity", Name: "Antigravity", User: "u@x.com",
		Windows: []QuotaWindow{
			{Name: "Gemini 3 Flash", Model: "gemini-3-flash", Family: "Gemini", Pool: "Gemini", Used: 85, ResetsAt: &reset},
			{Name: "Gemini 3.1 Pro (High)", Model: "gemini-3.1-pro-high", Family: "Gemini", Pool: "Gemini", Used: 85, ResetsAt: &reset},
			{Name: "Claude Opus 4.6 (Thinking)", Model: "claude-opus-4-6-thinking", Family: "Claude", Pool: "Claude & GPT", Used: 90},
			{Name: "GPT-OSS 120B (Medium)", Model: "gpt-oss-120b-medium", Family: "GPT-OSS", Pool: "Claude & GPT", Used: 90},
			{Name: "7 days", Pool: "Gemini", Span: week, Aside: true, Used: 85, ResetsAt: &reset},
			{Name: "5 hours", Pool: "Gemini", Span: five, Aside: true, Used: 95, ResetsAt: &reset},
			{Name: "7 days", Pool: "Claude & GPT", Span: week, Aside: true, Used: 90, ResetsAt: &reset},
			{Name: "5 hours", Pool: "Claude & GPT", Span: five, Aside: true, Used: 90, ResetsAt: &reset},
		}}
	got := quotaReport([]SubscriptionQuota{antigravity}, nil, nil, now)
	var names []string
	for _, w := range got[0].Windows {
		names = append(names, w.Name)
	}
	if strings.Join(names, ",") != "Gemini · 7 days,Gemini · 5 hours,Claude & GPT · 7 days,Claude & GPT · 5 hours" {
		t.Fatalf("windows %v", names)
	}
	// the pool's own use, label and reset reach the span
	w := got[0].Windows[0]
	if w.Used != 85 || w.Pool != "Gemini" || w.Remaining != 15 || w.ResetsAt == nil || !w.ResetsAt.Equal(reset) {
		t.Fatalf("span %+v", w)
	}
}
