package gui

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// #1515: the menu bar's tooltip names an account by the name the user gave
// it (or the key plan it stands in for), not only the card's name; the
// account in use is named once, by that name.
func TestTrayNamesTheAccountByItsAlias(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	at := now.Add(2 * time.Hour)
	q := provider.SubscriptionQuota{Provider: "zcode-plugin", Name: "ZCode", User: "31****36@qq.com", Alias: "某某·GLM高级版",
		Windows: []provider.QuotaWindow{{Name: "5 hours", Used: 12, ResetsAt: &at, Span: 5 * time.Hour}}}
	if _, tip := trayUsageText(q, now, false); !strings.HasPrefix(tip, "ZCode · 某某·GLM高级版\n") {
		t.Fatalf("tip %q", tip)
	}
	q.Error = "BigModel API keys: not JSON"
	if _, tip := trayUsageText(q, now, false); !strings.HasPrefix(tip, "ZCode · 某某·GLM高级版: ") {
		t.Fatalf("error tip %q", tip)
	}
	other := q
	other.User, other.Alias = "b@qq.com", ""
	trayInUseAccount = func(string) string { return "31****36@qq.com" }
	t.Cleanup(func() { trayInUseAccount = provider.InUseLogin })
	c, ok := trayInUseCard([]provider.SubscriptionQuota{other, q}, "zcode-plugin", now)
	if !ok {
		t.Fatal("no card")
	}
	if _, tip := trayUsageText(c, now, false); !strings.HasPrefix(tip, "ZCode · 某某·GLM高级版: ") {
		t.Fatalf("in use tip %q", tip)
	}
}
