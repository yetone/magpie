package provider

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// A reset shared by unrelated vendors is not evidence of a shared account.
func TestUnrelatedPlanWithSameResetStays(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reset := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	windows := []QuotaWindow{{Name: "Weekly", Span: 7 * 24 * time.Hour, ResetsAt: &reset}}
	for _, pair := range [][2]string{
		{"minimax", "codex"},
		{"zhipu", "claude"},
		{"zai", "codex"},
		{"kimi-code", "zcode"},
		{"minimax-cn", "zcode-plugin"},
	} {
		t.Run(pair[0]+"/"+pair[1], func(t *testing.T) {
			plan := SubscriptionQuota{Provider: pair[0], User: "plan-key", Windows: windows}
			plan.glmPlan = pair[0] == "zhipu" || pair[0] == "zai"
			sub := SubscriptionQuota{Provider: pair[1], User: "other@example.com", Windows: windows}
			got := notShown([]SubscriptionQuota{plan}, []SubscriptionQuota{sub})
			if !reflect.DeepEqual(got, []SubscriptionQuota{plan}) {
				t.Fatalf("unrelated plan disappeared: %+v", got)
			}
		})
	}
}

// A Zhipu plan read with a key of the account ZCode is signed in to is
// shown once, on ZCode's card; another account's plan stays.
func TestPlanOfASignedInAccountShownOnce(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	at := func(h int) *time.Time {
		x := time.UnixMilli(1790000000000).Add(time.Duration(h) * time.Hour)
		return &x
	}
	five, week := 5*time.Hour, 7*24*time.Hour
	zcode := SubscriptionQuota{Provider: "zcode", Windows: []QuotaWindow{
		{Name: "5 hours", Span: five, Used: 1, ResetsAt: at(2)},
		{Name: "Weekly", Span: week, Used: 32, ResetsAt: at(90)},
	}}
	same := SubscriptionQuota{Provider: "zhipu", glmPlan: true, Plan: "Max", Windows: []QuotaWindow{
		{Name: "5 hours", Span: five, Used: 4, ResetsAt: at(2)},
		{Name: "7 days", Span: week, Used: 32, ResetsAt: at(90)},
		{Name: "MCP · Month", Aside: true, ResetsAt: at(300)},
	}}
	other := SubscriptionQuota{Provider: "zai", glmPlan: true, Plan: "Pro", Windows: []QuotaWindow{
		{Name: "5 hours", Span: five, ResetsAt: at(3)},
		{Name: "7 days", Span: week, ResetsAt: at(90)},
	}}
	unknown := SubscriptionQuota{Provider: "zai", glmPlan: true, Plan: "Lite", Windows: []QuotaWindow{{Name: "5 hours", Span: five}}}
	got := notShown([]SubscriptionQuota{same, other, unknown}, []SubscriptionQuota{zcode})
	if len(got) != 2 || got[0].Provider != "zai" || got[1].Plan != "Lite" {
		t.Fatalf("%+v", got)
	}
	// ZCode not answering: still the seat they were found to be, on
	// ZCode's card (#1515; TestSeatKeptThroughAFailedRead)
	zcode.Error = "timeout"
	if got := notShown([]SubscriptionQuota{same}, []SubscriptionQuota{zcode}); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// Moved ZCode keeps its id; an independently installed plugin has the
// -plugin suffix. Both carry the same windows through quotaOfPlugin.
func TestGLMPlanMatchesZCodePlugin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reset := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"zcode", "zcode-plugin"} {
		t.Run(id, func(t *testing.T) {
			sub := quotaOfPlugin(SubscriptionQuota{Provider: id, User: "me@example.com"}, plugin.Usage{
				Windows: []plugin.UsageWindow{{Name: "Weekly", Span: 7 * 24 * 60 * 60, ResetsAt: reset.Format(time.RFC3339)}},
			})
			for _, provider := range []string{"zhipu", "zai", "custom-glm"} {
				plan := SubscriptionQuota{Provider: provider, glmPlan: true, User: "work key",
					Windows: []QuotaWindow{{Span: 7 * 24 * time.Hour, ResetsAt: &reset}}}
				if got := notShown([]SubscriptionQuota{plan}, []SubscriptionQuota{sub}); len(got) != 0 {
					t.Errorf("%s: duplicate plan remains: %+v", provider, got)
				}
			}
		})
	}
}

// Leaving a plan out doesn't touch the list it was left out of, which is
// PlanQuotas' cache.
func TestPlanLeftOutOfACopy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	r := time.Now()
	w := []QuotaWindow{{Span: time.Hour, ResetsAt: &r}}
	plans := []SubscriptionQuota{{Provider: "a", glmPlan: true, Windows: w}, {Provider: "b"}}
	if got := notShown(plans, []SubscriptionQuota{{Provider: "zcode", Windows: w}}); len(got) != 1 || got[0].Provider != "b" {
		t.Fatalf("filtered cards: %+v", got)
	}
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
