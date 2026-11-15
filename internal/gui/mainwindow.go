//go:build !nogui

package gui

import (
	"fmt"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/yetone/magpie/internal/settings"
)

// mainMacWindow: the main window's Mac options. A window belongs to the
// Space it was last shown on, and showing it again takes the user there; one
// first shown while another app was full screen (a restart to update, with
// the user in that app) belonged to that app's Space from then on, and every
// Open went back to it. It moves to the Space the user is on whenever it is
// shown instead, and can still be made full screen itself. Once shown it
// stays on its Space (showHere), so a click on the Dock icon goes to it there
// (reopenMain).
func mainMacWindow() application.MacWindow {
	return application.MacWindow{
		// no InvisibleTitleBarHeight: that strip drags from anywhere in
		// it, tabs included; the header marks what drags instead. On
		// macOS 26 and later its toolbar is taken off once it has placed
		// the traffic lights (plainTitlebar)
		TitleBar: application.MacTitleBarHiddenInset,
		CollectionBehavior: application.MacWindowCollectionBehaviorMoveToActiveSpace |
			application.MacWindowCollectionBehaviorFullScreenPrimary,
	}
}

// mainFrameless: the main window on Windows has no title bar of its own.
// The page's header is its title bar, as on the Mac, so a wallpaper or the
// page's colour runs to the top of the window instead of under a separate
// strip (258a14 on X); the page draws minimise, maximise and close
// (#wincap). Wails keeps the frame's styles, so the window still snaps,
// casts its shadow and has rounded corners on Windows 11.
func mainFrameless(goos string) bool { return goos == "windows" }

// captionJS tells the page whether the window is maximised, and keeps
// Wails' resize edges inside the page off while it is. A frameless window
// is resized by those edges (SM_CYSIZEFRAME pixels, Wails' drag.ts), which
// Wails arms whenever the window is resizable; maximised, they would take
// the top-right pixel from Close and the top row from the header's drag.
// Wails turns them on again once its script is ready, which may come
// before this or after, so its switch is wrapped once and remembers what
// it was last asked.
func captionJS(maximised bool) string {
	return fmt.Sprintf(`(()=>{const m=%t;window.magpieMaximised=m;const w=window._wails;`+
		`if(w&&typeof w.setResizable==="function"){if(!w.setResizable.edges){const f=w.setResizable,g=v=>{g.on=v;f(!!v&&!window.magpieMaximised)};g.edges=true;g.on=true;w.setResizable=g}w.setResizable(w.setResizable.on)}`+
		`if(typeof window.magpieCaption==="function")window.magpieCaption(m)})();`, maximised)
}

type closeAction int

const (
	closeHide            closeAction = iota // hide the window now
	closeLeaveFullscreen                    // leave full screen, then hide it
)

// closeStep: what closing the main window does. A full-screen window on the
// Mac hidden as it is leaves its Space behind, black, with nothing in it; it
// leaves full screen first and is hidden once it has.
func closeStep(goos string, fullscreen bool) closeAction {
	if goos == "darwin" && fullscreen {
		return closeLeaveFullscreen
	}
	return closeHide
}

// mainClosedJS tells the page its window was closed: an account shown
// while Hide accounts is on (its eye) is hidden again, as each is shown for
// the window's session only (yogurt36 on Discord). The page stays loaded
// while the window is hidden, so it can't tell by itself.
const mainClosedJS = "window.hideAccounts?.closed?.()"

// hideMain hides the main window, and takes magpie out of the Dock if it is
// there only while the window is shown.
func (h *host) hideMain() {
	application.InvokeSync(func() {
		if h.main != nil {
			h.main.ExecJS(mainClosedJS)
			hideWebview(h.main)
		}
	})
	h.dock(settings.Load(), false)
}

// leaveFullscreenThenHide takes the closed full-screen window out of full
// screen; WindowDidExitFullScreen hides it. Should that never come (the
// system refused to leave), it is hidden after a while all the same.
func (h *host) leaveFullscreenThenHide() {
	h.closing.Store(true)
	application.InvokeSync(func() {
		if h.main != nil {
			h.main.UnFullscreen()
		}
	})
	time.AfterFunc(3*time.Second, func() {
		if h.closing.Swap(false) {
			application.InvokeAsync(h.hideMain)
		}
	})
}
