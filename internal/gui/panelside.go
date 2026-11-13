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

// panelRefit is where an open panel of w by h goes when its page asks for
// another size, on Windows: on the place the tray gave it as it opened,
// at, with its centre and the edge nearest the taskbar held, kept inside
// the screen's work area. Placing it by the icon again moved it
// (jiangshangmuyun on X): a panel opened from the taskbar's overflow
// flyout was put flush right, as the icon's place wasn't in the taskbar
// then, and its first fit, a moment later with the flyout closed, put it
// over the flyout's arrow, a fair way left.
func panelRefit(at, work panelRect, w, h int) panelRect {
	r := panelRect{at.x + (at.w-w)/2, at.y, w, h}
	if panelOpenAt("windows", at, work, true) == "foot" {
		r.y = at.y + at.h - h
	}
	if work.w > 0 && work.h > 0 {
		r.x = min(max(r.x, work.x), work.x+work.w-w)
		r.y = min(max(r.y, work.y), work.y+work.h-h)
	}
	return r
}
