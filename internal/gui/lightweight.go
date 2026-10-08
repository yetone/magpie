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
	// the window opens at the size it was last given, no smaller than its
	// page's least at the text size (window_rule.go); maximised again, if it
	// was: on Linux as it is made, elsewhere once it is first shown
	// (placeMain)
	minW, minH := windowMin(z, 0, 0)
	width, height := openSize(settings.Load().Window, minW, minH)
	state := application.WindowStateNormal
	if runtime.GOOS == "linux" && settings.Load().WindowMaximised {
		state = application.WindowStateMaximised
	}
	// Windows' title bar in the page's colour from the first frame; the
	// page keeps it so as its theme changes (TintTitleBar)
	winOpts, winBg := windowChrome(cmp.Or(os.Getenv("MAGPIE_THEME"), settings.Load().Theme))
	w := h.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "magpie",
		URL:              url,
		Width:            width,
		Height:           height,
		MinWidth:         minW,
		MinHeight:        minH,
		Zoom:             z,
		Hidden:           true,
		StartState:       state,
		Mac:              mainMacWindow(),
		Windows:          winOpts,
		BackgroundColour: winBg,
	})
	// A resize is kept once it settles (settle): the size, or that the
	// window is maximised. Nothing is kept of a window not placed and shown
	// yet (placeMain: a text size can resize it, unmaximised as yet), one
	// let go (0×0), or one full screen or minimised, which keeps what was
	// kept before it went so; nor, on Linux, of one hidden by then, since
	// X11 drops a hidden window's maximised state.
	var resized *time.Timer
	w.OnWindowEvent(events.Common.WindowDidResize, func(*application.WindowEvent) {
		if resized != nil {
			resized.Stop()
		}
		resized = time.AfterFunc(500*time.Millisecond, func() {
			if h.placed.Load() != w || w.IsFullscreen() || w.IsMinimised() || runtime.GOOS == "linux" && !w.IsVisible() {
				return
			}
			wd, ht := w.Size()
			if wd == 0 {
				return
			}
			sw, sh := fitScreen(w)
			if s := settings.Load(); settle(&s, wd, ht, sw, sh, w.IsMaximised()) {
				settings.Save(s)
			}
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
				h.placeMain(w)
				showHere(w)
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
	h.placeMain(h.main)
	showHere(h.main)
}

// reopenMain answers a click on magpie's Dock icon (#1252). A window that is
// open, on whichever Space, is left there, and magpie is activated as any
// app is: the Mac takes the user to the window's Space. One closed or
// minimised is shown as openMain shows it, on the Space the user is on.
func (h *host) reopenMain() {
	if h.main == nil || h.loading[h.main] || !windowOpen(h.main) {
		h.openMain("")
		return
	}
	h.closing.Store(false) // reopened while leaving full screen: it stays
	if h.panel != nil {
		h.panel.Hide()
	}
	activateApp()
}

// spaceSettle is how long a window just shown keeps moving to the active
// Space: the Mac moves it once the order to the front is committed, after
// Show has returned, and not at all if the flag is gone by then.
const spaceSettle = 500 * time.Millisecond

// showHere shows w and makes it key on the Space the user is on, wherever it
// was last; on the main thread. It moves only while being shown: left on,
// activating magpie in any way (the Dock, Command-Tab) would pull the open
// window off its own Space onto the user's (#1252).
func showHere(w *application.WebviewWindow) {
	setMovesToActiveSpace(w, true)
	w.Show()
	w.Focus()
	time.AfterFunc(spaceSettle, func() {
		application.InvokeAsync(func() { setMovesToActiveSpace(w, false) })
	})
}

// placeMain puts the main window, made and not shown yet, as it was last
// left; on the main thread, before its first Show. On Windows, a size kept
// on a larger screen is fitted to this one's work area and centred: Windows
// doesn't, and the title bar would be out of reach. Then, if it was
// maximised, it is maximised again; on Linux it was made so (StartState),
// since GTK can't be asked about a window not shown yet.
//
// StartState can't do it elsewhere: on the Mac the window is centred after
// it is zoomed, and so isn't zoomed when shown; on Windows it shows the
// window before its page has come. Maximised while hidden, it zooms as it
// appears on the Mac, and Show (SW_SHOW) keeps it so on Windows. Restored,
// it goes back to the size it opened at. Wails' Maximise lets go of the
// window's least size until its own UnMaximise, which a restore from the
// Mac's title bar isn't; the least is put back at once. Before, too: the Mac
// makes a window a point short, under its least when its size is the
// least, and the resize to it then would stop the zoom.
//
// A Mac window larger than its screen is left to the Mac, which shows it
// fitted to the screen, and zoomed: zoomed here as well, it would restore
// to its own size, which the Mac fits to the screen again, so it couldn't
// be restored at all.
func (h *host) placeMain(w *application.WebviewWindow) {
	if h.placed.Swap(w) == w || runtime.GOOS == "linux" {
		return
	}
	if w.IsMaximised() {
		return
	}
	wd, ht := w.Size()
	sw, sh := h.mainRoom(w)
	fw, fh, fitted := fitRoom(wd, ht, sw, sh)
	if fitted && runtime.GOOS == "windows" {
		w.SetSize(fw, fh)
		w.Center()
		fitted = false
	}
	if settings.Load().WindowMaximised && !fitted {
		w.EnableSizeConstraints()
		w.Maximise()
		w.EnableSizeConstraints()
	}
}

// mainRoom is the work area of the screen the main window is on, in the
// units of its Size, 0s when unknown: DIPs on Windows, points on the Mac,
// where GetScreen gives it in pixels (Wails' cScreenToScreen; the Screen
// manager's copy is laid out only once the app has finished launching,
// after the window may first be shown).
func (h *host) mainRoom(w *application.WebviewWindow) (int, int) {
	switch runtime.GOOS {
	case "windows":
		return screenRoom(w)
	case "darwin":
		if s, err := w.GetScreen(); err == nil && s != nil && s.ScaleFactor > 0 {
			return int(float32(s.WorkArea.Width) / s.ScaleFactor), int(float32(s.WorkArea.Height) / s.ScaleFactor)
		}
	}
	return 0, 0
}

// fitScreen is the work area the main window is fitted to, in the units of
// its Size: Windows' (DIPs), 0s elsewhere — the Mac fits a window to its
// screen itself, and GTK's window manager does.
func fitScreen(w *application.WebviewWindow) (int, int) {
	if runtime.GOOS != "windows" {
		return 0, 0
	}
	return screenRoom(w)
}
