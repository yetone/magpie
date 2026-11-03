package gui

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// yi_time on Discord, magpie in Chinese on Windows (v0.1.2777): the tray
// icon's tooltip said "WorkBuddy / Credits 0 / 500 credits · 已用 0% · 27
// 天 13 小时 后重置". The window's name and unit come from the WorkBuddy
// plugin in English; the Usage page's card says them in the page's
// language (t(w.name), t(w.unit)), and the tooltip says them as the card
// does, in every language.
func TestTrayTooltipSaysWindowWordsAsTheCard(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	at := now.Add(27*24*time.Hour + 13*time.Hour + 20*time.Minute)
	card := provider.SubscriptionQuota{Provider: "workbuddy", Name: "WorkBuddy",
		Windows: []provider.QuotaWindow{{Name: "Credits", Used: 0, ResetsAt: &at, Display: "0 / 500", Amount: 0, Limit: 500, Unit: "credits"}}}
	want := map[string]string{
		"en":    "WorkBuddy\nCredits 0 / 500 credits · 0% used · resets in 27d 13h",
		"zh":    "WorkBuddy\n积分 0 / 500 积分 · 已用 0% · 27 天 13 小时 后重置",
		"zh-TW": "WorkBuddy\n積分 0 / 500 積分 · 已用 0% · 27 天 13 小時 後重設",
		"ja":    "WorkBuddy\nクレジット 0 / 500 クレジット · 使用済み 0% · 27日 13時間 後にリセット",
		"de":    "WorkBuddy\nCredits 0 / 500 Credits · 0% verbraucht · Reset in 27 T 13 h",
	}
	for lang, w := range want {
		if _, tip := trayCardText(lang, card, now, false, false, nil); tip != w {
			t.Errorf("%s tooltip =\n%q, want\n%q", lang, tip, w)
		}
	}

	// a pool's window, named with its pool (trayShared): the pool's name
	// is the vendor's, its window's the page's
	pooled := provider.SubscriptionQuota{Provider: "antigravity", Name: "Antigravity",
		Windows: []provider.QuotaWindow{{Name: "5 hours", Pool: "Gemini", Used: 10, Aside: true}}}
	if _, tip := trayCardText("zh", pooled, now, false, false, nil); !strings.Contains(tip, "Gemini · 5 小时 已用 10%") {
		t.Errorf("zh pooled tooltip = %q", tip)
	}
}

// The words plugins name and count their usage windows in are said in every
// language the page has, so neither the card nor the tray's tooltip shows
// one in English (the names and units in magpie-community/plugins'
// usage() results, 2026-11-03; a model's or a plan's own name stays as
// given).
func TestPluginWindowWordsSaidInEveryLanguage(t *testing.T) {
	words := []string{
		// units
		"credits", "tokens", "requests", "questions",
		// window names
		"Credits", "Extra credits", "Prepaid credits", "Free model tokens", // workbuddy
		"Add-on credits", "Dedicated credits", "Shared credits", "Total credits", // qoder
		"Monthly credits", "Purchased credits", "5-hour window", // agnes
		"Promotional Credits",                         // gcp-ai
		"Free models",                                 // devin
		"Free usage", "Monthly", "Included", "Weekly", // cursor
		"Free trial",                   // kiro
		"Dollar Usage",                 // trae
		"Neurons/day (free allowance)", // cloudflare-workers-ai
		"AI Q&A",                       // orcaterm
		"5 hours", "7 days",
	}
	for _, lang := range []string{"zh", "zh-TW", "ja", "de"} {
		for _, w := range words {
			if _, ok := pageWords()[lang][w]; !ok {
				t.Errorf("i18n.js's %s doesn't say %q", lang, w)
			}
		}
	}
}

// parsePageWords reads every entry of a block, two to a line too, with
// its escapes, and leaves out what isn't an entry.
func TestParsePageWords(t *testing.T) {
	src := "const I18N = {\n  zh: {\n    // a comment: \"not\": \"this\"\n    \"Repository\": \"仓库\", \"Branch\": \"分支\",\n    \"say \\\"{n}\\\"\": \"说「{n}」\",\n  },\n  \"zh-TW\": {\n    \"Credits\": \"積分\",\n  },\n};\nconst NOTICES = [\n  \"Credits\": \"no\",\n];\n"
	got := parsePageWords(src)
	if got["zh"]["Repository"] != "仓库" || got["zh"]["Branch"] != "分支" || got["zh"][`say "{n}"`] != "说「{n}」" || got["zh-TW"]["Credits"] != "積分" {
		t.Errorf("parsed %v", got)
	}
	if _, ok := got["zh"]["not"]; ok || len(got) != 2 {
		t.Errorf("parsed a comment or past the table: %v", got)
	}
}
