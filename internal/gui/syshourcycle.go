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

// hourCycleCache holds the answer for a while: the page asks at every load,
// and macOS's costs two processes.
var hourCycleCache struct {
	sync.Mutex
	at  time.Time
	val string
}

// systemHourCycle is "h23", "h12", or "" when the system names none.
var systemHourCycle = cachedHourCycle

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
