package gui

// The quick panel has one control that opens the main window, on the edge
// nearest the tray icon (isaac_bai on Discord: with Windows' taskbar at the
// bottom, the header's icon was the panel's whole height from the tray).
// "head" is the header's icon, for a panel that hangs below its icon (the
// Mac's menu bar, a taskbar or bar at the top); "foot" is a text button
// left of Quit, for a panel that stands above it (Windows' taskbar at the
// bottom, a bottom panel on Linux). One of them only: the panel had both
// until e0ad48641 (yogurt36 on Discord).

// panelRect is a window's or a screen's place, in one coordinate space.
type panelRect struct{ x, y, w, h int }

// panelOpenAt says which edge of the panel opens the main window: "foot"
// when the placed panel is nearer the screen's bottom than its top, "head"
// when nearer the top. The Mac's is always "head": its menu bar is at the
// top of every screen. A place that isn't known (Wayland, where a window
// can't read where the compositor put it, or a screen not read) goes by
// the system: Windows' taskbar is at the bottom unless moved, the Mac's
// menu bar is at the top, and Linux's bars are mostly at the top.
func panelOpenAt(goos string, panel, screen panelRect, known bool) string {
	if goos == "darwin" {
		return "head"
	}
	if known && panel.h > 0 && screen.h > 0 {
		above := panel.y - screen.y                        // room between the screen's top and the panel
		below := screen.y + screen.h - (panel.y + panel.h) // and between the panel and the screen's bottom
		if below < above {
			return "foot"
		}
		return "head"
	}
	if goos == "windows" {
		return "foot"
	}
	return "head"
}

// panelOpenAtJS tells the panel's page the side (app.js magpiePanelOpenAt).
func panelOpenAtJS(side string) string {
	if side != "foot" {
		side = "head"
	}
	return `window.magpiePanelOpenAt && window.magpiePanelOpenAt("` + side + `")`
}
