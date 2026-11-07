package provider

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// A plugin's row set aside with text and nothing counted (AutoClaw's
// 积分余额, Cline's balance, Cursor's Auto + Composer note) is that text:
// QuotaWindow.Text, carried to /api/quotas' span, so the GUI, TUI and CLI
// don't read its used 0 as "100% left". A window that counts, a 0 with a
// limit, and one not set aside (Cursor's On-demand "$0 / $20", a real 0%)
// are readings and stay so.
func TestPluginTextRowIsText(t *testing.T) {
	q := quotaOfPlugin(SubscriptionQuota{Provider: "autoclaw-plugin", User: "u"}, plugin.Usage{Windows: []plugin.UsageWindow{
		{Name: "积分余额", Display: "1,234 积分", Aside: true},
		{Name: "5 hours", Used: fptr(40), Display: "40 / 100"},
		{Name: "Credits", Display: "0 / 50", Limit: 50, Aside: true},
		{Name: "On-demand", Used: fptr(0), Display: "$0 / $20"},
		{Name: "Bare", Aside: true},
	}})
	want := []bool{true, false, false, false, false}
	for i, w := range q.Windows {
		if w.Text != want[i] {
			t.Errorf("%s: Text %v", w.Name, w.Text)
		}
	}
	r := quotaReport([]SubscriptionQuota{q}, nil, nil, time.Now())
	if len(r) != 1 || len(r[0].Windows) != 5 || !r[0].Windows[0].Text || r[0].Windows[1].Text {
		t.Fatalf("report %+v", r)
	}
	b, _ := json.Marshal(r[0].Windows[:2])
	if s := string(b); strings.Count(s, `"text":true`) != 1 {
		t.Fatalf("json %s", s)
	}
}

// Google AI Studio's plugin (magpie-gemini-web's ai-studio) reports no
// allowance, only the models its accounts cool down on, beside two lines
// about its local service: its proxy and its admin page. With nothing
// cooling those lines are all its card has, and each is text, which the
// menu bar has no figure for and Settings leaves out of its picker
// (LuckymanJC on Discord); a cooldown is a reading.
func TestAIStudioServiceLinesAreText(t *testing.T) {
	service := []plugin.UsageWindow{
		{Name: "代理出口", Display: "不走代理（未设置）"},
		{Name: "管理页", Display: "http://127.0.0.1:5000（用户名 admin）"},
	}
	q := quotaOfPlugin(SubscriptionQuota{Provider: "ai-studio", User: "a@gmail.com"}, plugin.Usage{Windows: service})
	for _, w := range q.Windows {
		if !w.Text {
			t.Errorf("%s: not text", w.Name)
		}
	}
	cooling := append([]plugin.UsageWindow{{Name: "所有模型", Used: fptr(100), ResetsAt: "2026-11-07T15:00:00Z", Display: "冷却中"}}, service...)
	q = quotaOfPlugin(SubscriptionQuota{Provider: "ai-studio", User: "a@gmail.com"}, plugin.Usage{Windows: cooling})
	if q.Windows[0].Text || q.Windows[0].Used != 100 {
		t.Fatalf("cooldown %+v", q.Windows[0])
	}
}
