package gui

import (
	"strings"
	"sync"
	"time"
)

// The clock the system is set to show, 12-hour or 24-hour, which no
// webview hands the page: macOS's "24-Hour Time" switch is not in
// navigator.language, so an en-US Mac set to 24 hours showed 12 (iXerol on
// X). boot.js tells the page as "h23" or "h12", or leaves it out when the
// system says nothing beyond its region's own, and the page asks its
// formatters for it (intlLang in i18n.js).
//
// The region is the system's too: a Mac set to English with Japan as its
// region (AppleLocale en_JP) reads 24 hours, but WKWebView's
// navigator.language says en-US and the page formatted in that (iXerol on
// X). boot.js tells the page the system's own tag as "locale".

// hourCycleCache holds the answer for a while: the page asks at every load,
// and macOS's costs two processes.
var hourCycleCache struct {
	sync.Mutex
	at  time.Time
	val string
}

// systemHourCycle is "h23", "h12", or "" when the system names none.
var systemHourCycle = cachedHourCycle

// systemLocale is the system's language and region as a BCP 47 tag
// ("en-JP"), or "" when it names none.
var systemLocale = cachedLocale

var localeCache struct {
	sync.Mutex
	at  time.Time
	val string
}

func cachedLocale() string {
	c := &localeCache
	c.Lock()
	defer c.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < 30*time.Second {
		return c.val
	}
	c.val, c.at = readLocale(), time.Now()
	return c.val
}

// localeTag turns a system's locale name into a BCP 47 tag: "en_JP",
// "en_JP.UTF-8", "en_JP@rg=gbzzzz" and "en-JP" all become a language and a
// region ("en-JP"). The C and POSIX locales, and anything that isn't a
// language, give "". A macOS "@rg=" keyword is the region the user picked
// apart from the language, and wins.
func localeTag(raw string) string {
	raw = strings.TrimSpace(raw)
	name, kw, _ := strings.Cut(raw, "@")
	if i := strings.IndexAny(name, ".:"); i >= 0 {
		name = name[:i]
	}
	parts := strings.Split(strings.ReplaceAll(name, "_", "-"), "-")
	lang := strings.ToLower(parts[0])
	if len(lang) < 2 || len(lang) > 3 || lang == "c" || lang == "posix" {
		return ""
	}
	for _, r := range lang {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	tag := lang
	region := ""
	for _, p := range parts[1:] {
		if len(p) == 2 && region == "" {
			region = strings.ToUpper(p)
		}
	}
	for _, k := range strings.Split(kw, ";") {
		if v, ok := strings.CutPrefix(k, "rg="); ok && len(v) >= 2 {
			region = strings.ToUpper(v[:2])
		}
	}
	if region != "" {
		tag += "-" + region
	}
	return tag
}

func cachedHourCycle() string {
	c := &hourCycleCache
	c.Lock()
	defer c.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < 30*time.Second {
		return c.val
	}
	c.val, c.at = readHourCycle(), time.Now()
	return c.val
}

// forcedHourCycle is macOS's pair of switches (AppleICUForce24HourTime and
// AppleICUForce12HourTime, as `defaults read -g` prints them: "1", or a
// failure when the key is not there). The 24-hour one wins, as ICU reads
// them.
func forcedHourCycle(force24, force12 string) string {
	switch {
	case strings.TrimSpace(force24) == "1":
		return "h23"
	case strings.TrimSpace(force12) == "1":
		return "h12"
	}
	return ""
}

// windowsHourCycle is the user's long time format (Control Panel →
// International, sTimeFormat): "HH:mm:ss" is 24 hours, "h:mm:ss tt" 12.
// Letters inside quotes are literal text, not a pattern.
func windowsHourCycle(format string) string {
	var in bool
	for _, r := range format {
		switch {
		case r == '\'':
			in = !in
		case in:
		case r == 'H':
			return "h23"
		case r == 'h':
			return "h12"
		}
	}
	return ""
}

// gnomeHourCycle is gsettings' org.gnome.desktop.interface clock-format:
// '24h' or '12h'.
func gnomeHourCycle(out string) string {
	switch strings.Trim(strings.TrimSpace(out), "'\"") {
	case "24h":
		return "h23"
	case "12h":
		return "h12"
	}
	return ""
}
