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

// pendingMain merges re-entrant openMain calls that arrive while makeMain is
// still running. On Windows, Wails' NewWithOptions pumps Win32 messages inside
// WebView2's Embed before returning, so a double-click or repeated tray click
// re-enters openMain while h.main is still nil.
type pendingMain struct {
	making    bool
	url       string
	quiet     bool
	minimised bool
}

// begin starts a main-window creation if none is in flight. When one already
// is, it merges the latest non-empty url and upgrades quiet/minimised reveal
// flags toward a visible focused open, then returns false.
func (p *pendingMain) begin(url string, quiet, minimised bool) bool {
	if p.making {
		if url != "" {
			p.url = url
		}
		if !quiet {
			p.quiet = false
			p.minimised = false
		} else if !minimised {
			p.minimised = false
		}
		return false
	}
	p.making = true
	p.url = ""
	p.quiet = quiet
	p.minimised = minimised
	return true
}

// finish ends an in-flight main-window creation and returns any url requested
// during it along with the merged reveal flags.
func (p *pendingMain) finish() (url string, quiet, minimised bool) {
	url, quiet, minimised = p.url, p.quiet, p.minimised
	*p = pendingMain{}
	return url, quiet, minimised
}

// cancel resets an in-flight creation if it returns early or panics.
func (p *pendingMain) cancel() { *p = pendingMain{} }

// pendingRemote merges re-entrant OpenRemote calls while NewWithOptions is
// still creating the remote window.
type pendingRemote struct {
	making bool
	link   string
	title  string
}

func (p *pendingRemote) begin(link, title string) bool {
	if p.making {
		p.link, p.title = link, title
		return false
	}
	p.making = true
	p.link, p.title = "", ""
	return true
}

func (p *pendingRemote) finish() (link, title string) {
	link, title = p.link, p.title
	*p = pendingRemote{}
	return link, title
}

func (p *pendingRemote) cancel() { *p = pendingRemote{} }

// mainClosingHides says whether a main window's WindowClosing hook should
// cancel the close and hide it instead of letting Wails destroy it. A window
// already marked gone or no longer held as h.main closes for good; isMain is
// skipped when gone is already true.
func mainClosingHides(gone bool, isMain func() bool) bool {
	if gone {
		return false
	}
	return isMain()
}
