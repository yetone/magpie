//go:build !darwin && !nogui

package gui

import "github.com/wailsapp/wails/v3/pkg/application"

// The Dock is the Mac's; elsewhere magpie stays in the tray.
func dockPolicy(bool) application.ActivationPolicy { return application.ActivationPolicyAccessory }
func setDock(bool, bool)                           {}
func dockOnFullscreen()                            {}

// glidePanel steps the shown panel to height a frame at a time, keeping it
// by the tray icon as it goes.
func (h *host) glidePanel(height int, g Glide) bool {
	w := h.panel
	_, from := w.Size()
	gen := h.glides.Add(1)
	go stepGlide(g, from, height, func() bool { return h.glides.Load() == gen }, func(v int) {
		application.InvokeSync(func() {
			if h.panel == w { // not let go meanwhile (lightweight mode)
				h.sizeOpenPanel(w, h.panelW(), v)
			}
		})
	})
	return true
}

// TintPanel: the page paints the panel's tint itself here.
func (h *host) TintPanel(c [4]uint8, ms int) bool { return false }
