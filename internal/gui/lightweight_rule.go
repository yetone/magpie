package gui

import "time"

// Lightweight mode (settings.Lightweight, #580): a window closed — the
// panel or the main window — has its webview let go once it has stayed
// closed a while, and is made again the next time it is opened. Its web
// content process (a hundred MB or so on a Mac) ends with it; the next open
// draws the page from the start, a moment's wait.

// lightEvery is how often the closed windows are looked at, and lightTicks
// how many looks in a row have to find one closed before it goes: a window
// is let go 30 to 60 s after it was closed, so a panel opened again soon
// after is the same one, at once.
const (
	lightEvery = 30 * time.Second
	lightTicks = 2
)

// lightStep is one look at a window: ticks is how many looks in a row have
// found it closed before this one. It says that count now and whether the
// window's webview is to go. With the mode off, or the window shown, the
// count starts over.
func lightStep(on, shown bool, ticks int) (int, bool) {
	if !on || shown {
		return 0, false
	}
	ticks++
	return ticks, ticks >= lightTicks
}

// panelOnFirstOpen says whether the tray panel is made the first time it is
// opened rather than as magpie starts: on the Mac, where a hidden panel's
// page kept a WebContent process of its own (~67 MB) for nothing.
func panelOnFirstOpen(goos string) bool { return goos == "darwin" }

// mainOnFirstOpen says whether the main window is made the first time it is
// shown rather than as magpie starts: on the Mac, when it starts without the
// window (showMain false: at login, for the panel or a link).
func mainOnFirstOpen(goos string, showMain bool) bool { return goos == "darwin" && !showMain }
