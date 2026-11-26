package gui

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/egress"
)

// The tray's tooltip begins with a warning while the Claude accounts' way
// out is in a region Anthropic doesn't list, in magpie's language; one in
// a supported region, or one not known, says nothing.
func TestTrayEgressTip(t *testing.T) {
	cn := egress.Result{State: egress.Unsupported, Loc: "CN"}
	for lang, want := range map[string]string{
		"en":    "outside Anthropic's supported regions",
		"zh":    "不在 Anthropic 支持的地区内",
		"zh-TW": "不在 Anthropic 支援的地區內",
		"ja":    "サポート対象地域外",
		"de":    "außerhalb der von Anthropic unterstützten Regionen",
	} {
		got := trayEgressTip(lang, cn)
		if !strings.Contains(got, want) || !strings.Contains(got, "CN") {
			t.Errorf("%s: %q", lang, got)
		}
		for _, w := range []string{"ban", "封", "安全", "sicher"} {
			if strings.Contains(got, w) {
				t.Errorf("%s: the warning says %q: %s", lang, w, got)
			}
		}
	}
	for _, r := range []egress.Result{{State: egress.OK, Loc: "US"}, {State: egress.Unknown}, {State: egress.Unknown, Loc: "XX"}} {
		if got := trayEgressTip("en", r); got != "" {
			t.Errorf("%+v warned %q", r, got)
		}
	}
}

// A way out outside the user's own list (mamba on Discord) says so, with
// its address, and never names Anthropic's list it wasn't judged by.
func TestTrayEgressTipMine(t *testing.T) {
	r := egress.Result{State: egress.Unsupported, Loc: "JP", IP: "203.0.113.9", Mine: true}
	for lang, want := range map[string]string{
		"en":    "outside the ways out you allowed",
		"zh":    "不在你允许的出口内",
		"zh-TW": "不在你允許的出口內",
		"ja":    "許可した出口に含まれていません",
		"de":    "keinen der von dir erlaubten Ausgänge",
	} {
		got := trayEgressTip(lang, r)
		if !strings.Contains(got, want) || !strings.Contains(got, "203.0.113.9 (JP)") || strings.Contains(got, "Anthropic") {
			t.Errorf("%s: %q", lang, got)
		}
	}
}
