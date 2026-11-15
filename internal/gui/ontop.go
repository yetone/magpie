package gui

import (
	"errors"
	"os"
	"runtime"

	"github.com/yetone/magpie/internal/settings"
)

// The main window's pin keeps it above other apps' windows, for Usage or
// Routing watched while working elsewhere (NovaStudiosss on X). The title
// bar's pin (app.js renderOnTop, shown where boot.js says pin) asks POST
// /api/window/ontop; the choice is this computer's own setting
// (settings.OnTop), put on the window as it is made (makeMain) and at once
// on the one showing.

// onTopper is a host whose main window can stay on top; a browser's page
// (magpie web) has no window of its own to pin.
type onTopper interface {
	// SetOnTop puts the main window above other windows, or back among
	// them.
	SetOnTop(on bool)
}

// canOnTop says whether a window can keep itself above others on goos: not
// in a Wayland session, which has no way for an app to ask it (Wails sets
// _NET_WM_STATE_ABOVE, an X11 hint), unless GTK speaks X11 there
// (GDK_BACKEND=x11), as with the window's place (keepsPlace).
func canOnTop(goos string, getenv func(string) string) bool {
	return keepsPlace(goos, getenv)
}

var errNoOnTop = errors.New("this window can't stay on top here")

// pinnable says whether w's main window can be pinned: the app's host
// (not a browser's page, magpie web's or the app's served to a browser) on
// a desktop that can keep a window on top.
func pinnable(w Windows) bool {
	_, ok := w.(onTopper)
	return ok && !isWeb(w) && canOnTop(runtime.GOOS, os.Getenv)
}

// pinMain saves the pin and puts it on the window; where the window can't
// be pinned (pinnable) nothing is saved.
func pinMain(w Windows, on bool) error {
	if !pinnable(w) {
		return errNoOnTop
	}
	p := w.(onTopper)
	if err := settings.SetOnTop(on); err != nil {
		return err
	}
	p.SetOnTop(on)
	return nil
}
