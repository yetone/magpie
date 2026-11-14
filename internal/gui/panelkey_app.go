//go:build !nogui

package gui

import (
	"errors"
	"log"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/yetone/magpie/internal/settings"
)

// errPanelKeyFailed is a refusal that isn't another app's hold: a key
// this keyboard has no code for, or no display to bind on.
var errPanelKeyFailed = errors.New("magpie couldn't take this shortcut")

// startPanelKey binds the saved shortcut once the app has started, and
// has Settings bind each one recorded after. A magpie this one took over
// from (before, its pid) may hold it until it has quit: refused, it is
// tried again then.
func (h *host) startPanelKey(before int) {
	onPanelShortcut = h.bindPanelKey
	go func() {
		<-h.ready
		s := settings.Shared().PanelShortcut
		if s == "" || !panelKeysState().Can {
			return
		}
		if h.bindPanelKey(s) != nil && before != 0 {
			awaitExit(before, handedOffWait)
			if settings.Shared().PanelShortcut == s {
				_ = h.bindPanelKey(s)
			}
		}
	}()
}

// bindPanelKey binds s ("" for none) in place of the shortcut bound now.
// Refused, the one before is bound again, and a saved one refused is
// said in Settings (panelKeyTaken) until one is bound.
func (h *host) bindPanelKey(s string) error {
	h.keyMu.Lock()
	defer h.keyMu.Unlock()
	gs := h.app.GlobalShortcut
	old := h.panelKey
	if old != "" && old != s {
		if err := gs.Unregister(old); err != nil {
			log.Println("panel shortcut:", err)
		}
		h.panelKey = ""
	}
	if s != "" && h.panelKey != s {
		if err := gs.Register(s, h.panelKeyPressed); err != nil {
			log.Println("panel shortcut:", err)
			if old != "" && old != s && gs.Register(old, h.panelKeyPressed) == nil {
				h.panelKey = old
			}
			if s == settings.Shared().PanelShortcut {
				panelKeyTaken.Store(s)
			}
			if strings.Contains(err.Error(), "already registered") {
				return errPanelKeyTaken
			}
			return errPanelKeyFailed
		}
		h.panelKey = s
	}
	panelKeyTaken.Store("")
	return nil
}

// dropPanelKey lets the shortcut go, for a Mac's magpie leaving the menu
// bar to the version it handed over to.
func (h *host) dropPanelKey() {
	h.keyMu.Lock()
	defer h.keyMu.Unlock()
	if h.panelKey != "" {
		_ = h.app.GlobalShortcut.Unregister(h.panelKey)
		h.panelKey = ""
	}
}

// panelKeyPressed opens the quick panel, or closes it, as a click on the
// tray icon does when it opens the panel. Wails calls it on a goroutine
// of its own; togglePanel goes to the main thread.
func (h *host) panelKeyPressed() {
	h.whenReady(func() { application.InvokeAsync(h.togglePanel) })
}
