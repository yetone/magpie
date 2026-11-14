package gui

import (
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// antigravityPools is an Antigravity card whose two pools each have a
// 5-hour and a weekly window: four windows, all set aside by routing.
func antigravityPools() provider.SubscriptionQuota {
	week, five := 7*24*time.Hour, 5*time.Hour
	return provider.SubscriptionQuota{Provider: "antigravity", Name: "Antigravity", Icon: "antigravity-color", User: "someone@gmail.com",
		Windows: []provider.QuotaWindow{
			{Name: "Weekly", Pool: "Gemini", Used: 12, Span: week, Aside: true},
			{Name: "5 hours", Pool: "Gemini", Used: 40, Span: five, Aside: true},
			{Name: "Weekly", Pool: "Claude & GPT", Used: 63, Span: week, Aside: true},
			{Name: "5 hours", Pool: "Claude & GPT", Used: 100, Span: five, Aside: true},
		}}
}

// Every window a card has is in the menu bar, not its first two
// (jen_celeus on X: Antigravity's four windows, two shown): the Mac draws
// them in columns of two (mpRows), the shortest first, so the first
// column is the 5-hour windows, the second the weekly ones. Windows
// picked in Settings › Menu bar windows are still the only ones shown,
// however many.
func TestTrayEveryWindow(t *testing.T) {
	now := time.Date(2026, 11, 15, 3, 0, 0, 0, time.UTC)
	q := antigravityPools()
	cells, label, _ := trayCardsView("en", []provider.SubscriptionQuota{q}, now, false, false, nil)
	if len(cells) != 1 || !slices.Equal(cells[0].Rows, []string{"40%", "100%", "12%", "63%"}) {
		t.Fatalf("cells %+v", cells)
	}
	if label != "40% · 100% · 12% · 63%" {
		t.Fatalf("label %q", label)
	}
	var kinds []string
	for _, w := range withTrayKinds([]provider.SubscriptionQuota{q})[0].Windows {
		kinds = append(kinds, w.TrayKind)
	}
	for _, tc := range []struct {
		pick []string
		rows []string
	}{
		{[]string{kinds[1], kinds[3], kinds[0]}, []string{"40%", "100%", "12%"}},
		{[]string{kinds[0], kinds[2]}, []string{"12%", "63%"}},
		{[]string{kinds[2]}, []string{"63%"}},
	} {
		cells, _, _ := trayCardsView("en", []provider.SubscriptionQuota{q}, now, false, false, map[string][]string{"antigravity": tc.pick})
		if len(cells) != 1 || !slices.Equal(cells[0].Rows, tc.rows) {
			t.Errorf("picked %q: rows %q, want %q", tc.pick, cells[0].Rows, tc.rows)
		}
	}
}
