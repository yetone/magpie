package gui

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A card whose windows are all text (Google AI Studio's plugin with
// nothing cooling: its proxy and admin page lines) has nothing the menu
// bar can draw: no label, and no window Settings could pick. Settings'
// picker leaves such a card out (app.js renderTrayUsage, the
// tray-text-only GUI test); ticked before, it drew nothing (LuckymanJC on
// Discord). A cooldown, a reading, is shown.
func TestTrayTextOnlyCardShowsNothing(t *testing.T) {
	now := time.Now()
	text := []provider.QuotaWindow{
		{Name: "代理出口", Display: "不走代理", Text: true, Aside: true},
		{Name: "管理页", Display: "http://127.0.0.1:5000", Text: true, Aside: true},
	}
	q := provider.SubscriptionQuota{Provider: "ai-studio", Name: "Google AI Studio", User: "a@gmail.com", Windows: text}
	if label, tip := trayCardText("en", q, now, false, false, nil); label != "" || tip != "" {
		t.Fatalf("text only: %q %q", label, tip)
	}
	for _, w := range withTrayKinds([]provider.SubscriptionQuota{q})[0].Windows {
		if w.TrayKind != "" {
			t.Fatalf("%s offered as %q", w.Name, w.TrayKind)
		}
	}
	reset := now.Add(time.Hour)
	q.Windows = append([]provider.QuotaWindow{{Name: "所有模型", Used: 100, ResetsAt: &reset, Display: "冷却中"}}, text...)
	if label, _ := trayCardText("en", q, now, false, false, nil); label != "100%" {
		t.Fatalf("cooling: %q", label)
	}
}
