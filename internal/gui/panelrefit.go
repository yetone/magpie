//go:build !nogui

package gui

import (
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// sizeOpenPanel gives the open panel its new size. On Windows the panel
// stays where the tray put it as it opened (panelRefit); elsewhere it is
// placed by the icon again, as before.
func (h *host) sizeOpenPanel(w *application.WebviewWindow, width, height int) {
	if runtime.GOOS != "windows" {
		w.SetSize(width, height)
		_ = h.tray.PositionWindow(w, 6)
		return
	}
	b := w.Bounds()
	var work panelRect
	if s, err := w.GetScreen(); err == nil && s != nil {
		work = panelRect{s.WorkArea.X, s.WorkArea.Y, s.WorkArea.Width, s.WorkArea.Height}
	}
	r := panelRefit(panelRect{b.X, b.Y, b.Width, b.Height}, work, width, height)
	w.SetBounds(application.Rect{X: r.x, Y: r.y, Width: r.w, Height: r.h})
}
