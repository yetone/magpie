//go:build !nogui

package gui

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// mendWebView has w's WebView2 failures handled (webviewmend.go) once its
// first page has come, by then its controller is made. Off Windows there
// is no WebView2 and onProcessFailed does nothing.
func (h *host) mendWebView(w *application.WebviewWindow) {
	var once sync.Once
	var mu sync.Mutex
	var last time.Time // the last reload or remake
	w.OnWindowEvent(events.Windows.WebViewNavigationCompleted, func(*application.WindowEvent) {
		once.Do(func() {
			application.InvokeAsync(func() {
				err := onProcessFailed(w, func(kind uint32) {
					what, m := webViewMendFor(kind)
					mu.Lock()
					now := time.Now()
					ok := mendAllowed(m, last, now)
					if ok && (m == mendReload || m == mendRemake) {
						last = now
					}
					mu.Unlock()
					if !ok {
						log.Printf("webview (%s window): %s again within %s; left as it is", w.Name(), what, mendGap)
						return
					}
					log.Printf("webview (%s window): %s; %s", w.Name(), what, mendWords[m])
					// out of WebView2's own callback
					application.InvokeAsync(func() { h.mend(w, m) })
				})
				if err != nil {
					log.Printf("webview (%s window): its process failures go unhandled: %v", w.Name(), err)
				}
			})
		})
	})
}

// mend does m for w; on the main thread.
func (h *host) mend(w *application.WebviewWindow, m webViewMend) {
	switch m {
	case mendRedraw:
		redrawShown(w)
	case mendReload:
		if err := reloadWebView(w); err != nil {
			log.Printf("webview (%s window): reload: %v", w.Name(), err)
		}
	case mendRemake:
		go h.remakeWindow(w)
	}
}

// redrawShown hides w's controller and shows it again, if w is open: what
// closing it to the tray and opening it again does, without the window
// going. On the main thread.
func redrawShown(w *application.WebviewWindow) {
	if !windowOpen(w) {
		return // hidden or minimised: its showing draws it anew
	}
	if err := webViewVisible(w, false); err != nil {
		log.Printf("webview (%s window): hide: %v", w.Name(), err)
		return
	}
	// a moment, so the hide is taken as one and not folded into the show
	time.AfterFunc(100*time.Millisecond, func() {
		application.InvokeAsync(func() {
			if !windowOpen(w) {
				return // closed or minimised meanwhile: Wails shows it when it opens
			}
			if err := webViewVisible(w, true); err != nil {
				log.Printf("webview (%s window): show: %v", w.Name(), err)
			}
		})
	})
}

// remakeWindow lets w go, as lightweight mode does, and makes the main
// window again at once if it was open; the panel is made again on its
// next open. Not on the main thread (winMu).
func (h *host) remakeWindow(w *application.WebviewWindow) {
	var open, main bool
	h.winMu.Lock()
	application.InvokeSync(func() {
		switch w {
		case h.main:
			main, open = true, windowUp(w)
			h.main = nil
			h.gone.Store(w) // its closing hook lets it close
		case h.panel:
			h.panel = nil
			if h.tray != nil {
				h.tray.AttachWindow(nil) // the untyped nil: no window
			}
		default:
			w = nil // let go already
		}
	})
	h.winMu.Unlock()
	if w == nil {
		return
	}
	w.Close()
	if main && open {
		// a new webview made while the old browser process is still
		// going down fails, and Wails ends magpie for it
		awaitBrowserGone()
		application.InvokeSync(func() {
			if h.main == nil {
				h.openMain("")
			}
		})
	}
}

// mendOnWake draws the open windows again after the computer wakes: a GPU
// that lost its device while asleep can leave WebView2's surface white. And
// when the screen comes back without a sleep, unlocked or the display on
// again (screenBackFrom, Windows only): a window first shown while nobody
// could see it may keep a white surface (aquaarboraurum on X, after each
// restart to update, which AutoRestart makes while the user is away).
func (h *host) mendOnWake() {
	h.app.Event.OnApplicationEvent(events.Common.SystemDidWake, func(*application.ApplicationEvent) {
		application.InvokeAsync(func() { h.redrawOpen("the computer woke") })
	})
	back := func(why string) { application.InvokeAsync(func() { h.redrawOpen(why) }) }
	screenBack.Store(&back)
	h.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		application.InvokeAsync(watchScreenBack)
	})
}

// screenBack is what the screen coming back does (mendOnWake), nil until
// the app is up.
var screenBack atomic.Pointer[func(why string)]

// redrawOpen draws each open window again (redrawShown), saying why in the
// log; on the main thread.
func (h *host) redrawOpen(why string) {
	for _, w := range []*application.WebviewWindow{h.main, h.panel} {
		if w != nil && windowOpen(w) {
			log.Printf("webview (%s window): %s; drawing it again", w.Name(), why)
			redrawShown(w)
		}
	}
}
