package gui

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Kimi Code's booster (加油包) is a wallet window set aside beside the plan's
// windows. The menu bar's tooltip says what is left on it, as the panel's
// card does under its rings (apple_test_src on X: it showed only on the
// Usage page); the label stays the plan's percent.
func TestTrayTipSaysWalletLeft(t *testing.T) {
	now := time.Now()
	r5, r7 := now.Add(2*time.Hour), now.Add(72*time.Hour)
	left := 98.5
	q := provider.SubscriptionQuota{Provider: "kimi-code", Name: "Kimi Code", User: "k@example.com", Windows: []provider.QuotaWindow{
		{Name: "5 hours", Used: 30, ResetsAt: &r5, Span: 5 * time.Hour},
		{Name: "Week", Used: 50, ResetsAt: &r7, Span: 7 * 24 * time.Hour},
		{Name: "Extra usage", Balance: &left, Unit: "CNY", Aside: true},
	}}
	label, tip := trayCardText("en", q, now, false, false, nil)
	if label != "30% · 50%" {
		t.Fatalf("label: %q", label)
	}
	if !strings.Contains(tip, "\nExtra usage 98.5 CNY left") {
		t.Fatalf("en tip: %q", tip)
	}
	_, tip = trayCardText("zh", q, now, false, false, nil)
	if strings.Contains(tip, "Extra usage") || !strings.Contains(tip, "剩余 98.5") {
		t.Fatalf("zh tip: %q", tip)
	}
}
