//go:build !nogui

package gui

import (
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// askQuitJS has the window's page put the question; askQuitURL opens a
// window made again on it.
const (
	askQuitJS  = "window.askQuit?.()"
	askQuitURL = "/?ask=quit"
)

// AskQuit is a Quit the user chose: at once while no agent is connected to
// the gateway this process serves or the user said not to ask, else the window comes up and asks
// (quitask.go). A second Quit within quitAgain of the question quits: the
// user has been told, and it is the way out should the window not show it.
func (h *host) AskQuit() {
	if quitAtOnce(h.quitAt.Load(), time.Now()) {
		h.app.Quit()
		return
	}
	h.quitAt.Store(time.Now().UnixNano())
	application.InvokeSync(func() {
		if h.main == nil || h.loading[h.main] {
			h.openMain(askQuitURL + h.query)
			return
		}
		h.openMain("")
		h.main.ExecJS(askQuitJS)
	})
}

// askingQuit puts an item of label in place of the Quit item of menu's
// app menu, with Command-Q, that calls ask; nil when menu has none.
func askingQuit(menu *application.Menu, label string, ask func()) *application.MenuItem {
	app := menu.FindByRole(application.AppMenu)
	if app == nil || app.GetSubmenu() == nil {
		return nil
	}
	sub := app.GetSubmenu()
	old := sub.FindByRole(application.Quit)
	if old == nil {
		return nil
	}
	sub.RemoveMenuItem(old)
	return sub.Add(label).SetAccelerator("CmdOrCtrl+q").OnClick(func(*application.Context) { ask() })
}
