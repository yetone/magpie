//go:build !nogui

package gui

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// #238: on Windows the frameless, translucent panel showed DWM's caption
// close X at its top right; its caption buttons are hidden there (no
// WS_SYSMENU), and left as they were on the Mac and Linux.
func TestPanelOptionsCaptionButtons(t *testing.T) {
	w := panelOptions("windows", "")
	if !w.Frameless || w.BackgroundType != application.BackgroundTypeTranslucent || !w.Windows.HiddenOnTaskbar {
		t.Fatalf("windows panel lost its frameless, translucent, off-taskbar options: %+v", w)
	}
	if w.CloseButtonState != application.ButtonHidden || w.MinimiseButtonState != application.ButtonHidden || w.MaximiseButtonState != application.ButtonHidden {
		t.Errorf("windows panel caption buttons = close %v min %v max %v, want all hidden", w.CloseButtonState, w.MinimiseButtonState, w.MaximiseButtonState)
	}
	if w.Windows.DisableFramelessWindowDecorations {
		t.Errorf("windows panel should keep its shadow and round corners")
	}
	for _, goos := range []string{"darwin", "linux"} {
		o := panelOptions(goos, "&theme=dark")
		if o.CloseButtonState != application.ButtonEnabled || o.MinimiseButtonState != application.ButtonEnabled || o.MaximiseButtonState != application.ButtonEnabled {
			t.Errorf("%s panel caption buttons changed: close %v min %v max %v", goos, o.CloseButtonState, o.MinimiseButtonState, o.MaximiseButtonState)
		}
		if o.URL != "/?mode=panel&theme=dark" || !o.Frameless || o.Mac.CornerRadius != 12 || o.Mac.Backdrop != application.MacBackdropTranslucent {
			t.Errorf("%s panel options changed: %+v", goos, o)
		}
	}
}
