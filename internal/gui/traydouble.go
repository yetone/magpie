package gui

import (
	"sync"
	"time"
)

// trayDoubleClicks says whether a double-click on the tray icon opens the
// main window on this system (isaac_bai on Discord). Only Windows' tray
// tells a double-click apart: Wails passes the icon's WM_LBUTTONDBLCLK on
// as OnDoubleClick there. The Mac's status item runs its action on the
// mouse-down, one call per click with no click count, and Linux's
// StatusNotifierItem has Activate alone, so a double-click there is two
// clicks: the panel open and closed again.
func trayDoubleClicks(goos string) bool { return goos == "windows" }

// trayDoubleRelease bounds how long after a double-click its own release
// may come. Windows sends a double-click as WM_LBUTTONUP, WM_LBUTTONDBLCLK
// and WM_LBUTTONUP again; that last one is the double-click's, not a click
// of its own, and would open the panel over the window just shown.
const trayDoubleRelease = 2 * time.Second

// trayDouble swallows the release that ends a double-click on the tray
// icon, once, and no click after it.
type trayDouble struct {
	mu    sync.Mutex
	until time.Time
}

// doubled records a double-click at now.
func (d *trayDouble) doubled(now time.Time) {
	d.mu.Lock()
	d.until = now.Add(trayDoubleRelease)
	d.mu.Unlock()
}

// swallow says the click at now is the release of a double-click, which
// does nothing; it is so only once per double-click.
func (d *trayDouble) swallow(now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.until.IsZero() {
		return false
	}
	ours := now.Before(d.until)
	d.until = time.Time{}
	return ours
}
