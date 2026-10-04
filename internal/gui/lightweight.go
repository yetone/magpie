//go:build !nogui

package gui

import (
	"cmp"
	"log"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/yetone/magpie/internal/settings"
)

// The windows, h.main and h.panel, are nil while lightweight mode has let
// them go (lightweight_rule.go). Both are read and set on the main thread
// only (application.InvokeSync, which runs at once when already on it), so
// a window is never used after it has gone. h.winMu is held, never on the
// main thread, around what has to reach a window from elsewhere and wait for
// the main thread itself (TintPanel's dispatch_sync), and around letting a
// window go.

// makeMain makes the main window, hidden, on url, with its hooks.
func (h *host) makeMain(url string) *application.WebviewWindow {
	z := h.zoom()
	// the window opens at the size it was last given
	width, height := 660, 600
	if s := settings.Load().Window; len(s) == 2 && s[0] >= 560 && s[1] >= 420 {
		width, height = s[0], s[1]
	}
	// and no smaller than its page's least at the text size
	minW, minH := windowMin(z, 0, 0)
	// Windows' title bar in the page's colour from the first frame; the
	// page keeps it so as its theme changes (TintTitleBar)
	winOpts, winBg := windowChrome(cmp.Or(os.Getenv("MAGPIE_THEME"), settings.Load().Theme))
	w := h.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "magpie",
		URL:              url,
		Width:            max(width, minW),
		Height:           max(height, minH),
		MinWidth:         minW,
		MinHeight:        minH,
		Zoom:             z,
		Hidden:           true,
		Mac:              mainMacWindow(),
		Windows:          winOpts,
		BackgroundColour: winBg,
	})
	// A resize is kept once it settles; a maximised or full-screen window
	// is the screen's size, not one the user gave it.
	var resized *time.Timer
	w.OnWindowEvent(events.Common.WindowDidResize, func(*application.WindowEvent) {
		if resized != nil {
			resized.Stop()
		}
		resized = time.AfterFunc(500*time.Millisecond, func() {
			if w.IsMaximised() || w.IsFullscreen() || w.IsMinimised() {
				return
			}
			wd, ht := w.Size()
			if wd < 560 || ht < 420 {
				return
			}
			// macOS reports a window a pixel short of the size it was
			// opened at; kept as it is, the window would shrink a pixel at
			// every start
			s := settings.Load()
			if len(s.Window) == 2 && abs(s.Window[0]-wd) <= 2 && abs(s.Window[1]-ht) <= 2 {
				return
			}
			s.Window = []int{wd, ht}
			settings.Save(s)
		})
	})
	// Closing the window keeps the tray alive; quitting is a menu action.
	// One lightweight mode lets go is closed for good.
	w.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		if h.gone.Load() == w {
			return
		}
		e.Cancel()
		if closeStep(runtime.GOOS, w.IsFullscreen()) == closeLeaveFullscreen {
			h.leaveFullscreenThenHide()
			return
		}
		h.hideMain()
	})
	// Out of full screen, magpie leaves the Dock again if it was there only
	// for that (dockOnFullscreen).
	w.OnWindowEvent(events.Mac.WindowDidExitFullScreen, func(*application.WindowEvent) {
		if h.closing.Swap(false) {
			h.hideMain()
			return
		}
		h.dock(settings.Load(), true)
	})
	return w
}

// makePanel makes the tray panel, at the height it last had, and hangs it
// on the tray icon.
func (h *host) makePanel() *application.WebviewWindow {
	z := h.zoom()
	po := panelOptions(runtime.GOOS, h.query)
	po.Width, po.Height, po.Zoom = h.panelW(), cmp.Or(h.panelHeight, zoomed(panelStart, z)), z
	w := h.app.Window.NewWithOptions(po)
	if h.tray != nil {
		h.tray.AttachWindow(w).WindowOffset(6)
	}
	// the icon stays lit while the panel is open (the Mac's)
	trayOwnClicks()
	w.OnWindowEvent(events.Mac.WindowShow, func(*application.WindowEvent) { trayHighlight(true) })
	w.OnWindowEvent(events.Mac.WindowHide, func(*application.WindowEvent) { trayHighlight(false) })
	return w
}

// madeAgain gives a window made again after lightweight mode let it go
// what the start gave the
// first: the Mac's text size (Windows' and Linux's took it with the
// options), Linux's title bar or the panel's name for Hyprland.
func (h *host) madeAgain(w *application.WebviewWindow) {
	if runtime.GOOS == "darwin" && h.zoom() != 1 {
		setPageZoom(w, h.zoom())
	}
	if w.Name() == "panel" {
		nameWindow(w, panelTitle)
	} else {
		plainTitlebar(w)
	}
}

// whenLoaded runs fn on the main thread once w can be shown: at once, but on
// Windows only once its page has come, since Wails shows a WebView2 window
// 3 s after Show whether or not its controller is made (see markReady).
func (h *host) whenLoaded(w *application.WebviewWindow, fn func()) {
	if runtime.GOOS != "windows" {
		fn()
		return
	}
	// until then it isn't shown by anything else (openMain, togglePanelNow):
	// a WebView2 window shown before its page has come is drawn black, and
	// on a slow start can crash
	if h.loading == nil {
		h.loading = map[*application.WebviewWindow]bool{}
	}
	h.loading[w] = true
	var once sync.Once
	w.OnWindowEvent(events.Windows.WebViewNavigationCompleted, func(*application.WindowEvent) {
		once.Do(func() {
			application.InvokeAsync(func() {
				delete(h.loading, w)
				fn()
			})
		})
	})
}

// lighten looks at the closed windows every lightEvery and lets go of one
// closed long enough while lightweight mode is on (#580).
func (h *host) lighten() {
	<-h.ready
	var ticks [2]int // the main window's, the panel's
	for range time.Tick(lightEvery) {
		on := settings.Load().Lightweight
		var let []*application.WebviewWindow
		h.winMu.Lock()
		application.InvokeSync(func() {
			for i, wp := range []**application.WebviewWindow{&h.main, &h.panel} {
				w := *wp
				if w == nil || h.loading[w] {
					ticks[i] = 0
					continue
				}
				shown := w.IsVisible() || i == 0 && h.closing.Load()
				var release bool
				if ticks[i], release = lightStep(on, shown, ticks[i]); !release {
					continue
				}
				ticks[i] = 0
				dropWebView(w)
				*wp = nil
				if i == 0 {
					h.gone.Store(w) // its closing hook lets it close
				} else if h.tray != nil {
					h.tray.AttachWindow(nil) // the untyped nil: no window
				}
				let = append(let, w)
			}
		})
		h.winMu.Unlock()
		for _, w := range let {
			log.Printf("lightweight: the %s window's webview let go", w.Name())
			w.Close()
		}
	}
}

// panelWin is the panel, made again if lightweight mode let it go; on the
// main thread. again says it was.
func (h *host) panelWin() (w *application.WebviewWindow, again bool) {
	if h.panel == nil {
		h.panel = h.makePanel()
		h.madeAgain(h.panel)
		return h.panel, true
	}
	return h.panel, false
}

// mainShown says whether the main window is up; on the main thread.
func (h *host) mainShown() bool { return h.main != nil && h.main.IsVisible() }

// openMain shows the main window on url ("" where it is), making it again
// if lightweight mode let it go; on the main thread.
func (h *host) openMain(url string) {
	h.closing.Store(false) // opened again while leaving full screen: it stays
	if h.panel != nil {
		h.panel.Hide()
	}
	h.dock(settings.Load(), true)
	if h.main == nil {
		w := h.makeMain(cmp.Or(url, "/?"+h.query))
		h.main = w
		h.madeAgain(w)
		h.whenLoaded(w, func() {
			if h.main == w {
				w.Show()
				w.Focus()
			}
		})
		return
	}
	if url != "" {
		h.main.SetURL(url)
	}
	if h.loading[h.main] {
		return // made again, it is shown once its page has come
	}
	h.main.Show()
	h.main.Focus()
}
