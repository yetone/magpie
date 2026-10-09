package gui

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/gateway"
)

// trayZh is the tray menu in Chinese; the page has its own words
// (i18n.js), and these few are all the menu shows (#301).
var trayZh = map[string]string{
	"Open magpie":                          "打开 magpie",
	"Version %s":                           "版本 %s",
	"Restart to Update":                    "重启以更新",
	"Restart to Update to %s":              "重启以更新到 %s",
	"Quit magpie":                          "退出 magpie",
	"Restart Now to Update":                "立即重启以更新",
	"Restart Now to Update (%d in flight)": "立即重启以更新（%d 个进行中）",
}

// trayZhTW is the tray menu in Traditional Chinese, with Taiwan's words.
var trayZhTW = map[string]string{
	"Open magpie":                          "開啟 magpie",
	"Version %s":                           "版本 %s",
	"Restart to Update":                    "重新啟動以更新",
	"Restart to Update to %s":              "重新啟動以更新到 %s",
	"Quit magpie":                          "結束 magpie",
	"Restart Now to Update":                "立即重新啟動以更新",
	"Restart Now to Update (%d in flight)": "立即重新啟動以更新（%d 個進行中）",
}

// trayJa is the tray menu in Japanese.
var trayJa = map[string]string{
	"Open magpie":                          "magpie を開く",
	"Version %s":                           "バージョン %s",
	"Restart to Update":                    "再起動してアップデート",
	"Restart to Update to %s":              "再起動して %s にアップデート",
	"Quit magpie":                          "magpie を終了",
	"Restart Now to Update":                "今すぐ再起動してアップデート",
	"Restart Now to Update (%d in flight)": "今すぐ再起動してアップデート（%d 件処理中）",
}

// trayDe is the tray menu in German.
var trayDe = map[string]string{
	"Open magpie":                          "magpie öffnen",
	"Version %s":                           "Version %s",
	"Restart to Update":                    "Zum Aktualisieren neu starten",
	"Restart to Update to %s":              "Zum Aktualisieren auf %s neu starten",
	"Quit magpie":                          "magpie beenden",
	"Restart Now to Update":                "Jetzt neu starten und aktualisieren",
	"Restart Now to Update (%d in flight)": "Jetzt neu starten und aktualisieren (%d laufend)",
}

// trayWords are the tray menu's translations by language.
var trayWords = map[string]map[string]string{"zh": trayZh, "zh-TW": trayZhTW, "ja": trayJa, "de": trayDe}

// onLang relabels the tray menu when the Settings page changes the
// language; set by the process that has the tray.
var onLang func()

// trayLang is the language the tray menu is in, as the page picks its own:
// the setting, or with "system" (or none) the system's: Traditional Chinese
// for Taiwan, Hong Kong, Macau and any zh-Hant (zh_TW.UTF-8, zh-Hant-TW),
// Simplified for any other zh, Japanese for any ja, German for any de.
func trayLang(pref string, system func() string) string {
	switch pref {
	case "en", "zh", "zh-TW", "ja", "de":
		return pref
	}
	switch sys := strings.ReplaceAll(strings.ToLower(system()), "_", "-"); {
	case zhTraditional(sys):
		return "zh-TW"
	case strings.HasPrefix(sys, "zh"):
		return "zh"
	case strings.HasPrefix(sys, "ja"):
		return "ja"
	case strings.HasPrefix(sys, "de"):
		return "de"
	}
	return "en"
}

// zhTraditional says a lowercased, dashed system tag is Traditional
// Chinese, as the page's sysLocale does.
func zhTraditional(sys string) bool {
	for _, p := range []string{"zh-tw", "zh-hk", "zh-mo", "zh-hant"} {
		if sys == p || strings.HasPrefix(sys, p+"-") || strings.HasPrefix(sys, p+".") || strings.HasPrefix(sys, p+"@") {
			return true
		}
	}
	return false
}

// notesLang is the language release notes are asked in for lang: there are
// no German ones, so German asks for the English, and Traditional Chinese
// asks for the Chinese ones.
func notesLang(lang string) string {
	switch lang {
	case "de":
		return "en"
	case "zh-TW":
		return "zh"
	}
	return lang
}

// trayText is a menu line in the language, English where it has none.
func trayText(lang, key string, args ...any) string {
	if s, ok := trayWords[lang][key]; ok {
		key = s
	}
	if len(args) == 0 {
		return key
	}
	return fmt.Sprintf(key, args...)
}

// trayLabels are the tray menu's lines: open, version, restart and quit.
type trayLabels struct{ open, version, restart, quit string }

// trayMenuLabels are the lines in the language; update is the version
// waiting for a restart, if there is one.
func trayMenuLabels(lang, version, update string) trayLabels {
	l := trayLabels{
		open:    trayText(lang, "Open magpie"),
		version: trayText(lang, "Version %s", version),
		restart: trayText(lang, "Restart to Update"),
		quit:    trayText(lang, "Quit magpie"),
	}
	if update != "" {
		l.restart = trayText(lang, "Restart to Update to %s", update)
	}
	return l
}

// trayRestartNow is the restart item while a restart waits for the
// gateway (#577): a click restarts at once, cutting short what b has in
// flight.
func trayRestartNow(lang string, b gateway.Busy) string {
	if n := b.Requests + b.Tools; n > 0 {
		return trayText(lang, "Restart Now to Update (%d in flight)", n)
	}
	return trayText(lang, "Restart Now to Update")
}

// envLang is the language the environment names: the first of LC_ALL,
// LC_MESSAGES, LANG and LANGUAGE set to one.
func envLang(getenv func(string) string) string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"} {
		if v := getenv(k); v != "" && v != "C" && v != "POSIX" && !strings.HasPrefix(v, "C.") {
			return v
		}
	}
	return ""
}

// firstAppleLang is the first entry `defaults read -g AppleLanguages`
// prints: ( "zh-Hans-CN", "en-US" ), one to a line, quoted or not.
func firstAppleLang(out string) string {
	for _, line := range strings.Split(out, "\n") {
		s := strings.Trim(strings.TrimSpace(line), `",`)
		if s != "" && s != "(" && s != ")" {
			return s
		}
	}
	return ""
}
