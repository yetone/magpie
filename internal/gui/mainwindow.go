//go:build !nogui

package gui

import (
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

// hideMain hides the main window, and takes magpie out of the Dock if it is
// there only while the window is shown.
func (h *host) hideMain() {
	application.InvokeSync(func() {
		if h.main != nil {
			h.main.Hide()
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
