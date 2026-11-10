//go:build !nogui

package gui

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/yetone/magpie/internal/settings"
)

// OpenRemote shows another magpie's page (remote.go) in a window of its
// own, link carrying its key; the window open already is pointed at link
// and brought up. It is an ordinary titled window: the page is that
// magpie's web page, which draws no title bar of its own.
func (h *host) OpenRemote(link, title string) {
	application.InvokeSync(func() {
		h.dockFront(settings.Load(), true, true)
		if w := h.remote; w != nil {
			w.SetTitle(title)
			w.SetURL(link)
			showHere(w, true)
			return
		}
		minW, minH := windowMin(h.zoom(), 0, 0)
		w := h.app.Window.NewWithOptions(application.WebviewWindowOptions{
			Name:      "remote",
			Title:     title,
			URL:       link,
			Width:     max(minW, 1100),
			Height:    max(minH, 760),
			MinWidth:  minW,
			MinHeight: minH,
			Zoom:      h.zoom(),
		})
		h.remote = w
		// closed, it is gone; the next Open makes it again
		w.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
			application.InvokeAsync(func() {
				if h.remote == w {
					h.remote = nil
				}
				h.dock(settings.Load(), h.mainShown())
			})
		})
		w.Center()
		showHere(w, true)
	})
}
