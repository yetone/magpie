package gui

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func TestTrayUsageText(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	in := func(d time.Duration) *time.Time { at := now.Add(d); return &at }

	// the shortest window first, whatever order the vendor gives them in;
	// on-demand spending and a single model's window are left out
	q := provider.SubscriptionQuota{Provider: "claude", Name: "Claude", User: "a@b.c", Windows: []provider.QuotaWindow{
		{Name: "Weekly", Used: 17.6, ResetsAt: in(3*24*time.Hour + 4*time.Hour), Span: 7 * 24 * time.Hour},
		{Name: "5-hour", Used: 42.2, ResetsAt: in(2*time.Hour + 10*time.Minute), Span: 5 * time.Hour},
		{Name: "Opus weekly", Used: 90, Model: "opus", Span: 7 * 24 * time.Hour},
		{Name: "Extra usage", Used: 5, Aside: true},
	}}
	label, tip := trayUsageText(q, now, false)
	if label != "42% · 18%" {
		t.Errorf("label %q", label)
	}
	if want := "Claude\n5-hour 42.2% used · resets in 2h 10m\nWeekly 17.6% used · resets in 3d 4h"; tip != want {
		t.Errorf("tip %q, want %q", tip, want)
	}
	// or what is left of each, as Settings or the Usage page says (#122)
	label, tip = trayUsageText(q, now, true)
	if label != "58% · 82%" {
		t.Errorf("left label %q", label)
	}
	if want := "Claude\n5-hour 57.8% left · resets in 2h 10m\nWeekly 82.4% left · resets in 3d 4h"; tip != want {
		t.Errorf("left tip %q, want %q", tip, want)
	}
	if id := trayCardID(q); id != "claude|a@b.c" {
		t.Errorf("id %q", id)
	}

	// no lengths known: the vendor's order, two at most, clamped, its own count
	q = provider.SubscriptionQuota{Provider: "zcode", Name: "ZCode", Windows: []provider.QuotaWindow{
		{Name: "5 小时", Used: 120, Display: "1.2k / 1k"},
		{Name: "Weekly", Used: -3, ResetSecs: 90},
		{Name: "Monthly", Used: 1},
	}}
	label, tip = trayUsageText(q, now, false)
	if label != "100% · 0%" {
		t.Errorf("label %q", label)
	}
	if want := "ZCode\n5 小时 1.2k / 1k · 100% used\nWeekly 0% used · resets in 2m\nMonthly 1% used"; tip != want {
		t.Errorf("tip %q, want %q", tip, want)
	}
	if id := trayCardID(q); id != "zcode" {
		t.Errorf("id %q", id)
	}

	q = provider.SubscriptionQuota{Name: "Copilot", Windows: []provider.QuotaWindow{{Name: "Premium", Used: 187418.3 / 218000 * 100}}}
	label, tip = trayUsageText(q, now, false)
	if label != "86%" || tip != "Copilot\nPremium 86.0% used" {
		t.Errorf("copilot used: %q %q", label, tip)
	}
	label, tip = trayUsageText(q, now, true)
	if label != "14%" || tip != "Copilot\nPremium 14.0% left" {
		t.Errorf("copilot left: %q %q", label, tip)
	}

	// a balance, an error, nothing
	if label, _ = trayUsageText(provider.SubscriptionQuota{Name: "DeepSeek", Balance: "¥12.30"}, now, false); label != "¥12.30" {
		t.Errorf("balance label %q", label)
	}
	// one read a while ago, standing in for one that couldn't be read now,
	// says when it was read (#420)
	read := time.Date(2026, 9, 27, 9, 5, 0, 0, time.Local)
	if label, tip = trayUsageText(provider.SubscriptionQuota{Name: "Relay", Balance: "$4.20", AsOf: &read}, now, false); label != "$4.20" || tip != "Relay · $4.20\nas of Sep 27 09:05, couldn't be read just now" {
		t.Errorf("balance as of: %q %q", label, tip)
	}
	if _, tip = trayUsageText(provider.SubscriptionQuota{Name: "Kimi", AsOf: &read, Windows: []provider.QuotaWindow{{Name: "Weekly", Used: 10}}}, now, false); tip != "Kimi\nWeekly 10% used\nas of Sep 27 09:05, couldn't be read just now" {
		t.Errorf("windows as of: %q", tip)
	}
	if label, tip = trayUsageText(provider.SubscriptionQuota{Name: "Codex", Error: "signed out"}, now, false); label != "" || tip != "Codex: signed out" {
		t.Errorf("error: %q %q", label, tip)
	}
	if label, tip = trayUsageText(provider.SubscriptionQuota{Name: "Empty"}, now, false); label != "" || tip != "" {
		t.Errorf("empty: %q %q", label, tip)
	}
}
