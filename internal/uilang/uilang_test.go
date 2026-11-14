package uilang

import "testing"

// Pick follows the setting, and with "system" (or none) the system's
// language, as the GUI page's sysLocale does (#301).
func TestPick(t *testing.T) {
	en := func() string { return "en-US" }
	for _, c := range []struct {
		pref, sys, want string
	}{
		{"zh", "en-US", "zh"},
		{"en", "zh-Hans-CN", "en"},
		{"system", "zh-Hans-CN", "zh"},
		{"", "zh_CN.UTF-8", "zh"},
		{"system", "", "en"},
		{"system", "ja-JP", "ja"},
		{"", "de_AT.UTF-8", "de"},
		{"system", "zh-Hant-TW", "zh-TW"},
		{"", "zh_HK", "zh-TW"},
		{"system", "zh-Hans-HK", "zh"},
		{"bogus", "fr-FR", "en"},
	} {
		if got := Pick(c.pref, func() string { return c.sys }); got != c.want {
			t.Errorf("Pick(%q, %q) = %s, want %s", c.pref, c.sys, got, c.want)
		}
	}
	if got := Pick("de", en); got != "de" {
		t.Errorf("Pick(de) = %s", got)
	}
	env := map[string]string{"LC_ALL": "C", "LANG": "zh_CN.UTF-8"}
	if got := Env(func(k string) string { return env[k] }); got != "zh_CN.UTF-8" {
		t.Errorf("Env skips C: %q", got)
	}
	if got := firstAppleLang("(\n    \"zh-Hans-CN\",\n    \"en-US\"\n)\n"); got != "zh-Hans-CN" {
		t.Errorf("firstAppleLang: %q", got)
	}
	if got := firstAppleLang("(\n    en,\n    \"zh-Hans\"\n)\n"); got != "en" {
		t.Errorf("firstAppleLang unquoted: %q", got)
	}
}
