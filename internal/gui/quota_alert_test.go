package gui

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func TestAlertText(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.Local)
	reset := time.Date(2026, 10, 1, 14, 30, 0, 0, time.Local)
	w := provider.QuotaAlert{Provider: "claude", Name: "Claude Code", User: "a@b.c", Window: "5 hours", Used: 82.34, ResetsAt: &reset}
	b := provider.QuotaAlert{Provider: "deepseek", Name: "DeepSeek", Balance: "¥4.20"}
	for _, c := range []struct {
		lang        string
		a           provider.QuotaAlert
		left        bool
		title, body string
	}{
		{"en", w, false, "Claude Code · a@b.c", "5 hours: 82.3% used, resets 14:30"},
		{"en", w, true, "Claude Code · a@b.c", "5 hours: 17.7% left, resets 14:30"},
		{"zh", w, false, "Claude Code · a@b.c", "5 小时窗口已用 82.3%，14:30 重置"},
		{"zh", w, true, "Claude Code · a@b.c", "5 小时窗口剩余 17.7%，14:30 重置"},
		{"en", b, false, "DeepSeek", "Balance down to ¥4.20 (alert at 5)"},
		{"zh", b, false, "DeepSeek", "余额已降至 ¥4.20（提醒线 5）"},
		{"de", w, false, "Claude Code · a@b.c", "5 Stunden: 82,3% verbraucht, Zurücksetzung um 14:30"},
		{"de", w, true, "Claude Code · a@b.c", "5 Stunden: 17,7% übrig, Zurücksetzung um 14:30"},
		{"de", b, false, "DeepSeek", "Guthaben auf ¥4.20 gesunken (Warnschwelle 5)"},
	} {
		title, body := alertText(c.lang, c.a, 5, c.left, now)
		if title != c.title || body != c.body {
			t.Errorf("%s left=%v: %q / %q, want %q / %q", c.lang, c.left, title, body, c.title, c.body)
		}
	}
	tomorrow := reset.Add(24 * time.Hour)
	week := provider.QuotaAlert{Name: "Codex", Window: "7 days", Used: 80, ResetsAt: &tomorrow}
	if _, body := alertText("zh", week, 0, false, now); body != "7 天窗口已用 80%，明天 14:30 重置" {
		t.Errorf("zh tomorrow: %q", body)
	}
	if _, body := alertText("ja", week, 0, false, now); body != "7 日枠を 80% 使用、明日 14:30 にリセット" {
		t.Errorf("ja tomorrow: %q", body)
	}
	if _, body := alertText("ja", week, 0, true, now); body != "7 日枠の残り 20%、明日 14:30 にリセット" {
		t.Errorf("ja left: %q", body)
	}
	if _, body := alertText("de", week, 0, false, now); body != "7 Tage: 80% verbraucht, Zurücksetzung morgen um 14:30" {
		t.Errorf("de tomorrow: %q", body)
	}
}
