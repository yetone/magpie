//go:build !darwin && !windows

package gui

import (
	"os"

	"github.com/yetone/magpie/internal/proc"
)

// readHourCycle asks GNOME (and what follows its setting: Cinnamon,
// Budgie, Omarchy's GNOME apps) for the clock format; other desktops say
// nothing, and the page keeps its locale's own.
func readHourCycle() string {
	out, err := proc.Command("gsettings", "get", "org.gnome.desktop.interface", "clock-format").Output()
	if err != nil {
		return ""
	}
	return gnomeHourCycle(string(out))
}

// readLocale is the locale the session sets for times (LC_ALL, LC_TIME,
// then LANG), as every GTK app reads it.
func readLocale() string {
	for _, k := range []string{"LC_ALL", "LC_TIME", "LANG"} {
		if v := os.Getenv(k); v != "" {
			return localeTag(v)
		}
	}
	return ""
}
