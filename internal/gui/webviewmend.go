package gui

import (
	"fmt"
	"time"
)

// A WebView2 whose process fails was left as it fell: nothing in Wails
// handles ProcessFailed, and nothing was logged. aquaarboraurum on X
// (0.1.4040, Windows 11, WebView2 138): after a while the main window went
// white but for its title bar, a sliver at the left edge and the status
// bar, with nothing in the log, and closing it to the tray and opening it
// again brought it back. Closing and opening hides the WebView2 controller
// and shows it again, which has it draw its surface anew. magpie now does
// what each failure needs (webViewMendFor) and logs it.

// webViewMend is what magpie does for a failed WebView2 process.
type webViewMend int

const (
	mendNothing webViewMend = iota
	// mendRedraw hides the controller and shows it again: what the tray's
	// close and open did by hand. WebView2 starts a GPU process again by
	// itself, but a window that stays shown may keep the surface the old
	// one drew, white.
	mendRedraw
	// mendReload reloads the page: a renderer that ended is replaced with
	// an error page, one that hangs draws nothing more.
	mendReload
	// mendRemake makes the window again: a browser process that ended
	// closes its webviews for good.
	mendRemake
)

// COREWEBVIEW2_PROCESS_FAILED_KIND's values (WebView2.h).
const (
	wv2BrowserExited       = 0
	wv2RenderExited        = 1
	wv2RenderUnresponsive  = 2
	wv2FrameRenderExited   = 3
	wv2UtilityExited       = 4
	wv2SandboxHelperExited = 5
	wv2GPUExited           = 6
	wv2PPAPIPluginExited   = 7
	wv2PPAPIBrokerExited   = 8
	wv2UnknownExited       = 9
)

// webViewMendFor names a ProcessFailed kind and says what is done for it.
// A process whose end leaves the page as it was (a utility process, a
// sandbox helper, a plugin) needs nothing; an unknown one gets a redraw,
// which costs nothing and is what brought the white window back.
func webViewMendFor(kind uint32) (string, webViewMend) {
	switch kind {
	case wv2BrowserExited:
		return "browser process exited", mendRemake
	case wv2RenderExited:
		return "render process exited", mendReload
	case wv2RenderUnresponsive:
		return "render process unresponsive", mendReload
	case wv2FrameRenderExited:
		return "a frame's render process exited", mendReload
	case wv2UtilityExited:
		return "utility process exited", mendNothing
	case wv2SandboxHelperExited:
		return "sandbox helper process exited", mendNothing
	case wv2GPUExited:
		return "GPU process exited", mendRedraw
	case wv2PPAPIPluginExited, wv2PPAPIBrokerExited:
		return "plugin process exited", mendNothing
	case wv2UnknownExited:
		return "a process exited", mendRedraw
	}
	return fmt.Sprintf("process failed (kind %d)", kind), mendRedraw
}

// mendGap is how soon after a reload or a remake of a window another is
// not made: a page that brings its renderer down again as it loads would
// otherwise be reloaded without end. The failure is still logged.
const mendGap = 30 * time.Second

// mendAllowed says whether a mend may run at now, the last heavy one
// (reload or remake) having run at last (zero: never). A redraw always may.
func mendAllowed(m webViewMend, last, now time.Time) bool {
	if m != mendReload && m != mendRemake {
		return true
	}
	return last.IsZero() || now.Sub(last) >= mendGap
}

// mendWords say each mend in the log.
var mendWords = map[webViewMend]string{
	mendNothing: "nothing to do",
	mendRedraw:  "drawing it again",
	mendReload:  "reloading the page",
	mendRemake:  "making the window again",
}
