// Package uilang is the language magpie speaks in: the one the user picked
// in Settings (settings.Lang), or with "system" the system's. The GUI's
// page picks its own the same way (i18n.js setLocale and sysLocale); the
// tray, notices and the TUI ask Pick.
package uilang

import "strings"

// Pick is the language for the setting pref: pref itself when it names
// one, else, with "system" (or none), the system's: Traditional Chinese
// for Taiwan, Hong Kong, Macau and any zh-Hant (zh_TW.UTF-8, zh-Hant-TW),
// Simplified for any other zh, Japanese for any ja, German for any de,
// English for the rest.
func Pick(pref string, system func() string) string {
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

// Env is the language the environment names: the first of LC_ALL,
// LC_MESSAGES, LANG and LANGUAGE set to one.
func Env(getenv func(string) string) string {
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
