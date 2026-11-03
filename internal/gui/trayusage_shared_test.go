package gui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// antigravityStarter is an Antigravity Starter account's card as magpie
// read it (quotas.json, 10-31, the account renamed): a window for each
// model, and the two pools' 7 days, which routing sets aside; no window
// over the whole account.
func antigravityStarter() provider.SubscriptionQuota {
	week := 7 * 24 * time.Hour
	gem := time.Date(2026, 11, 4, 7, 24, 29, 0, time.UTC)
	cla := time.Date(2026, 11, 2, 16, 12, 46, 0, time.UTC)
	q := provider.SubscriptionQuota{Provider: "antigravity", Name: "Antigravity", Icon: "antigravity-color",
		Plan: "Antigravity Starter Quota", User: "someone@gmail.com"}
	for _, m := range []struct{ name, id, family string }{
		{"Claude Opus 4.6 (Thinking)", "claude-opus-4-6-thinking", "Claude"},
		{"Claude Sonnet 4.6 (Thinking)", "claude-sonnet-4-6", "Claude"},
		{"Gemini 2.5 Flash", "gemini-2.5-flash", "Gemini"},
		{"Gemini 3 Flash", "gemini-3-flash", "Gemini"},
		{"Gemini 3.8 Flash (High)", "gemini-3.8-flash-high", "Gemini"},
		{"GPT-OSS 120B (Medium)", "gpt-oss-120b-medium", "GPT-OSS"},
	} {
		w := provider.QuotaWindow{Name: m.name, Model: m.id, Family: m.family, Used: 6.27976, ResetsAt: &gem}
		if m.family != "Gemini" {
			w.Used, w.ResetsAt = 100, &cla
		}
		q.Windows = append(q.Windows, w)
	}
	q.Windows = append(q.Windows,
		provider.QuotaWindow{Name: "7 days", Pool: "Gemini", Used: 6.27976, ResetsAt: &gem, Span: week, Aside: true},
		provider.QuotaWindow{Name: "7 days", Pool: "Claude & GPT", Used: 100, ResetsAt: &cla, Span: week, Aside: true})
	return q
}

// A card ticked for the menu bar is there whatever windows it has
// (nivekxft on X: Antigravity ticked, no Antigravity in the menu bar).
// Antigravity's pools stand in for windows over the account, named with
// their pool in the tooltip.
func TestTrayAntigravityPools(t *testing.T) {
	now := time.Date(2026, 10, 31, 3, 0, 0, 0, time.UTC)
	q := antigravityStarter()
	cells, label, tip := trayCardsView("en", []provider.SubscriptionQuota{q}, now, true, false, nil)
	if len(cells) != 1 || !slices.Equal(cells[0].Rows, []string{"94%", "0%"}) || cells[0].ID != "antigravity|someone@gmail.com" {
		t.Fatalf("cells %+v", cells)
	}
	if label != "94% · 0%" {
		t.Fatalf("label %q", label)
	}
	for _, s := range []string{"Gemini · 7 days 93.7% left", "Claude & GPT · 7 days 0% left"} {
		if !strings.Contains(tip, s) {
			t.Errorf("tooltip lacks %q:\n%s", s, tip)
		}
	}
	if strings.Contains(tip, "Gemini 2.5 Flash") {
		t.Errorf("the tooltip lists the models the pools stand for:\n%s", tip)
	}
	// Settings is offered each pool's window, by its pool as the two have
	// one span, and each model's window too, which the card shows only
	// when picked; the card's own windows are left as the cache has them
	out := withTrayKinds([]provider.SubscriptionQuota{q})
	var kinds []string
	for _, w := range out[0].Windows {
		if w.TrayKind != "" {
			kinds = append(kinds, w.Pool+"="+w.TrayKind)
		}
	}
	if !slices.Equal(kinds, []string{"=name:claude opus 4.6 (thinking)", "=name:claude sonnet 4.6 (thinking)", "=name:gemini 2.5 flash", "=name:gemini 3 flash",
		"=name:gemini 3.8 flash (high)", "=name:gpt-oss 120b (medium)", "Gemini=7d:gemini · 7 days", "Claude & GPT=7d:claude & gpt · 7 days"}) {
		t.Fatalf("kinds %q", kinds)
	}
	// the Gemini pool alone
	if cells, _, _ := trayCardsView("en", []provider.SubscriptionQuota{q}, now, true, false, map[string][]string{"antigravity": {"7d:gemini · 7 days"}}); !slices.Equal(cells[0].Rows, []string{"94%"}) {
		t.Fatalf("the Gemini pool alone: %q", cells[0].Rows)
	}
	if q.Windows[6].Name != "7 days" || q.Windows[6].TrayKind != "" {
		t.Fatalf("the cache's window was written: %+v", q.Windows[6])
	}
}

// With no pool to read (Gemini CLI's card, or Antigravity's when the
// summary can't be read) the models' own windows stand in: one a family,
// its tightest, the most used first.
func TestTrayModelWindows(t *testing.T) {
	now := time.Date(2026, 10, 31, 3, 0, 0, 0, time.UTC)
	q := antigravityStarter()
	q.Windows = slices.DeleteFunc(q.Windows, func(w provider.QuotaWindow) bool { return w.Pool != "" })
	q.Windows[3].Used = 40 // Gemini 3 Flash, the family's tightest
	cells, _, tip := trayCardsView("en", []provider.SubscriptionQuota{q}, now, false, false, nil)
	if len(cells) != 1 || !slices.Equal(cells[0].Rows, []string{"100%", "100%"}) {
		t.Fatalf("cells %+v", cells)
	}
	for _, s := range []string{"Claude Opus 4.6 (Thinking) 100% used", "Gemini 3 Flash 40% used", "GPT-OSS · GPT-OSS 120B"} {
		if s == "GPT-OSS · GPT-OSS 120B" {
			if strings.Contains(tip, s) {
				t.Errorf("a family its model's name says is said twice:\n%s", tip)
			}
			continue
		}
		if !strings.Contains(tip, s) {
			t.Errorf("tooltip lacks %q:\n%s", s, tip)
		}
	}
	if strings.Contains(tip, "Gemini 2.5 Flash") {
		t.Errorf("more than one window a family:\n%s", tip)
	}

	gemini := provider.SubscriptionQuota{Provider: "gemini", Name: "Gemini CLI", Windows: []provider.QuotaWindow{
		{Name: "gemini-2.5-pro", Model: "gemini-2.5-pro", Used: 12},
		{Name: "gemini-2.5-flash", Model: "gemini-2.5-flash", Used: 30},
	}}
	if cells, _, _ := trayCardsView("en", []provider.SubscriptionQuota{gemini}, now, false, false, nil); len(cells) != 1 || !slices.Equal(cells[0].Rows, []string{"30%", "12%"}) {
		t.Fatalf("gemini %+v", cells)
	}
}

// A plan's windows that bought credits carry the account past (Ollama's,
// all set aside) are still the plan's to watch; money aside, a row of
// text, or an account's own windows beside them are as before.
func TestTraySetAsideWindows(t *testing.T) {
	now := time.Date(2026, 10, 31, 3, 0, 0, 0, time.UTC)
	ollama := provider.SubscriptionQuota{Provider: "ollama", Name: "Ollama", Windows: []provider.QuotaWindow{
		{Name: "Weekly", Used: 30, Span: 7 * 24 * time.Hour, Aside: true},
		{Name: "Session limit", Used: 10, Aside: true},
	}}
	if cells, _, _ := trayCardsView("en", []provider.SubscriptionQuota{ollama}, now, false, false, nil); len(cells) != 1 || !slices.Equal(cells[0].Rows, []string{"30%", "10%"}) {
		t.Fatalf("ollama %+v", cells)
	}
	text := provider.SubscriptionQuota{Provider: "autoclaw", Name: "AutoClaw", Windows: []provider.QuotaWindow{
		{Name: "积分余额", Display: "120", Text: true, Aside: true},
		{Name: "Extra usage", Display: "$3.00", Aside: true},
	}}
	if cells, _, tip := trayCardsView("en", []provider.SubscriptionQuota{text}, now, false, false, nil); len(cells) != 0 || tip != "" {
		t.Fatalf("text rows made a cell: %+v %q", cells, tip)
	}
	claude := provider.SubscriptionQuota{Provider: "claude", Name: "Claude", Windows: []provider.QuotaWindow{
		{Name: "5 hours", Used: 42, Span: 5 * time.Hour},
		{Name: "Extra", Used: 90, Span: 30 * 24 * time.Hour, Aside: true},
		{Name: "Opus", Used: 80, Span: 7 * 24 * time.Hour, Model: "opus"},
	}}
	if cells, _, _ := trayCardsView("en", []provider.SubscriptionQuota{claude}, now, false, false, nil); len(cells) != 1 || !slices.Equal(cells[0].Rows, []string{"42%"}) {
		t.Fatalf("claude %+v", cells)
	}
}
