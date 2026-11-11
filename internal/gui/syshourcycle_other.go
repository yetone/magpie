//go:build !darwin && !windows

package gui

import "github.com/yetone/magpie/internal/proc"

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
