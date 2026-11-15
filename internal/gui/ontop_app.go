//go:build !nogui

package gui

import "github.com/wailsapp/wails/v3/pkg/application"

// SetOnTop pins the main window that shows, if one does; one lightweight
// mode let go is made again with the pin (makeMain).
func (h *host) SetOnTop(on bool) {
	application.InvokeSync(func() {
		if h.main != nil {
			h.main.SetAlwaysOnTop(on)
		}
	})
}
