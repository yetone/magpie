package gui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// The tray menu is in the page's language: the setting, or the system's
// when it follows the system (#301).
func TestTrayMenuLabels(t *testing.T) {
	zhSys := func() string { return "zh-Hans-CN" }
	enSys := func() string { return "en-US" }
	for _, c := range []struct {
		pref string
		sys  func() string
		want string
	}{
		{"zh", enSys, "zh"},
		{"en", zhSys, "en"},
		{"system", zhSys, "zh"},
		{"system", enSys, "en"},
		{"", func() string { return "zh_CN.UTF-8" }, "zh"},
		{"system", func() string { return "" }, "en"},
		{"ja", enSys, "ja"},
		{"system", func() string { return "ja-JP" }, "ja"},
		{"", func() string { return "ja_JP.UTF-8" }, "ja"},
		{"de", enSys, "de"},
		{"system", func() string { return "de-DE" }, "de"},
		{"", func() string { return "de_AT.UTF-8" }, "de"},
	} {
		if got := trayLang(c.pref, c.sys); got != c.want {
			t.Errorf("trayLang(%q, %s) = %s, want %s", c.pref, c.sys(), got, c.want)
		}
	}
	de := trayMenuLabels("de", "0.1.500", "0.1.501")
	if de != (trayLabels{"magpie öffnen", "Version 0.1.500", "Zum Aktualisieren auf 0.1.501 neu starten", "magpie beenden"}) {
		t.Errorf("de: %+v", de)
	}
	for lang, want := range map[string]string{"de": "en", "en": "en", "zh": "zh"} {
		if got := notesLang(lang); got != want {
			t.Errorf("notesLang(%q) = %q, want %q", lang, got, want)
		}
	}
	zh := trayMenuLabels("zh", "0.1.500", "")
	if zh != (trayLabels{"打开 magpie", "版本 0.1.500", "重启以更新", "退出 magpie"}) {
		t.Errorf("zh: %+v", zh)
	}
	if ja := trayMenuLabels("ja", "0.1.500", ""); ja != (trayLabels{"magpie を開く", "バージョン 0.1.500", "再起動してアップデート", "magpie を終了"}) {
		t.Errorf("ja: %+v", ja)
	}
	if l := trayMenuLabels("zh", "0.1.500", "0.1.501"); l.restart != "重启以更新到 0.1.501" {
		t.Errorf("zh restart: %q", l.restart)
	}
	en := trayMenuLabels("en", "0.1.500", "0.1.501")
	if en != (trayLabels{"Open magpie", "Version 0.1.500", "Restart to Update to 0.1.501", "Quit magpie"}) {
		t.Errorf("en: %+v", en)
	}
	env := map[string]string{"LC_ALL": "C", "LANG": "zh_CN.UTF-8"}
	if got := envLang(func(k string) string { return env[k] }); got != "zh_CN.UTF-8" {
		t.Errorf("envLang skips C: %q", got)
	}
	if got := firstAppleLang("(\n    \"zh-Hans-CN\",\n    \"en-US\"\n)\n"); got != "zh-Hans-CN" {
		t.Errorf("firstAppleLang: %q", got)
	}
	if got := firstAppleLang("(\n    en,\n    \"zh-Hans\"\n)\n"); got != "en" {
		t.Errorf("firstAppleLang unquoted: %q", got)
	}
}

// A language changed on the Settings page relabels the tray menu; a save
// that leaves it as it was doesn't.
func TestSettingsLangRelabelsTray(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	if err := settings.Save(settings.Settings{Lang: "en"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	onLang = func() { calls++ }
	t.Cleanup(func() { onLang = nil })
	post := func(body string) {
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	post(`{"theme":"dark","lang":"en"}`)
	if calls != 0 {
		t.Fatalf("relabelled %d times with the language unchanged", calls)
	}
	post(`{"theme":"dark","lang":"zh"}`)
	if calls != 1 {
		t.Fatalf("relabelled %d times after en → zh, want 1", calls)
	}
	if l := trayMenuLabels(trayLang(settings.Load().Lang, systemLang), "1", ""); l.quit != "退出 magpie" {
		t.Fatalf("after the change the menu reads %+v", l)
	}
}
