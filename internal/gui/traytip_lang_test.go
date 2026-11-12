package gui

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// yi_time on Discord: magpie set to Chinese on Windows, and the tray icon's
// tooltip said "Today: 121K tokens (2.9K in · 180 out · 118K cached)" and
// "3 requests" in English. The tooltip is in magpie's language, as the
// tray menu is: the idle tip, magpie's totals and a subscription's
// windows, in every language magpie has. A window's name and the numbers
// are the card's own.
func TestTrayTooltipFollowsMagpiesLanguage(t *testing.T) {
	// yi_time's numbers
	today := usage.Totals{Calls: 3, Input: 2_900, Output: 180, CacheRead: 118_000}
	if got, want := trayIdleTip("zh", today, "usd", 0), "magpie\n今天：121K token（输入 2.9K · 输出 180 · 缓存 118K）\n3 个请求"; got != want {
		t.Errorf("zh idle tip =\n%q, want\n%q", got, want)
	}
	if got, want := trayIdleTip("en", today, "usd", 0), "magpie\nToday: 121K tokens (2.9K in · 180 out · 118K cached)\n3 requests"; got != want {
		t.Errorf("en idle tip =\n%q, want\n%q", got, want)
	}
	if got, want := trayTotalCard("zh", "usage:today", today, "usd", 0).Name, "magpie 今日\n121K token（输入 2.9K · 输出 180 · 缓存 118K）\n3 个请求"; got != want {
		t.Errorf("zh total card =\n%q, want\n%q", got, want)
	}

	now := time.Now()
	asOf := now.Add(-time.Hour)
	card := provider.SubscriptionQuota{Provider: "claude", Name: "Claude", AsOf: &asOf,
		Windows: []provider.QuotaWindow{{Name: "5h", Used: 42, ResetSecs: 3*3600 + 10*60}}}
	_, tip := trayCardText("zh", card, now, false, false, nil)
	for _, w := range []string{"Claude\n", "5h 已用 42%", "3 小时 10 分 后重置", "截至 ", "暂时无法获取最新额度"} {
		if !strings.Contains(tip, w) {
			t.Errorf("zh card tooltip %q lacks %q", tip, w)
		}
	}
	if _, tip := trayCardText("zh", card, now, true, false, nil); !strings.Contains(tip, "5h 剩余 58%") {
		t.Errorf("zh card tooltip, what is left: %q", tip)
	}

	english := []string{"Today", "tokens", "K in", "0 in", " out", "cached", "request", " used", " left", "resets", "pace ", "as of", "Last 7", "no known price", "couldn't", "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	for _, lang := range []string{"zh", "zh-TW", "ja", "de"} {
		priced := today
		priced.Cost, priced.Unpriced = 1.5, 1
		readAt := now.Add(-2 * time.Hour)
		paced := card
		paced.ReadAt = &readAt
		paced.Windows = []provider.QuotaWindow{{Name: "5 hours", Used: 42.5, ResetSecs: 3*3600 + 10*60}, {Name: "7 days", Used: 3, ResetSecs: 4*86400 + 3600}, {Name: "x", Used: 1, ResetSecs: 120}}
		tips := []string{
			trayIdleTip(lang, priced, "usd", 0),
			trayIdleTip(lang, usage.Totals{Calls: 1, Input: 5}, "usd", 0),
			trayIdleTip(lang, usage.Totals{}, "usd", 0),
			trayTotalCard(lang, "usage:today", priced, "usd", 0).Name,
			trayTotalCard(lang, "usage:7d", priced, "usd", 0).Name,
		}
		for _, left := range []bool{false, true} {
			_, tip := trayCardText(lang, paced, now, left, true, nil)
			if pace := trayText(lang, "pace %s", ""); !strings.Contains(tip, pace) {
				t.Errorf("%s: no %q in %q", lang, pace, tip)
			}
			tips = append(tips, tip)
		}
		_, _, view := trayCardsView(lang, []provider.SubscriptionQuota{trayTotalCard(lang, "usage:today", priced, "usd", 0), paced}, now, false, true, nil)
		tips = append(tips, view)
		for _, tip := range tips {
			for _, w := range english {
				if strings.Contains(tip, w) {
					t.Errorf("%s tooltip says %q in English:\n%s", lang, w, tip)
				}
			}
		}
	}
}

// jipairamen on X: Settings' number unit was 万/亿, and the menu bar still
// said K and M. The tray's counts follow it as the Usage page's do, in
// Chinese only (the unit means nothing in English), 億 and 萬 in Traditional.
func TestTrayCountFollowsChineseUnits(t *testing.T) {
	old := trayChineseUnits
	t.Cleanup(func() { trayChineseUnits = old })
	n := 133_400_000
	for _, c := range []struct {
		units bool
		lang  string
		n     int
		want  string
	}{
		{true, "zh", n, "1.33 亿"},
		{true, "zh", 68_100, "6.8 万"},
		{true, "zh", 9_999, "9999"},
		{true, "zh-TW", n, "1.33 億"},
		{true, "zh-TW", 681_000, "68.1 萬"},
		{true, "en", n, "133M"},
		{true, "ja", n, "133M"},
		{false, "zh", n, "133M"},
	} {
		trayChineseUnits = func() bool { return c.units }
		if got := trayCount(c.lang, c.n); got != c.want {
			t.Errorf("trayCount(%q, %d) with units %v = %q, want %q", c.lang, c.n, c.units, got, c.want)
		}
	}
	trayChineseUnits = func() bool { return true }
	today := usage.Totals{Calls: 3, Input: 2_900, Output: 180, CacheRead: 118_000_000}
	if got := trayTotalCard("zh", "usage:today", today, "usd", 0); !strings.HasPrefix(got.Balance, "Σ1.18 亿") || !strings.Contains(got.Name, "1.18 亿 token") {
		t.Errorf("total card = %q / %q", got.Balance, got.Name)
	}
}
