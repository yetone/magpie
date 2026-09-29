//go:build !nogui

package gui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/shortcut"
	"github.com/yetone/magpie/internal/stats"
	"github.com/yetone/magpie/internal/update"
)

//go:embed tray.png
var trayIcon []byte // black glyph, tinted by the macOS menu bar

// trayFlap is the bird beating its wing and flicking its tail, played on
// the tray icon when it is clicked (build/icon/gen.go tray-flap).
//
//go:embed trayflap/*.png
var trayFlap embed.FS

//go:embed icon.png
var appIcon []byte // coloured, for other trays

// The app's own icon: the Mac's Dock (which it replaces the bundle's .icns
// in, at up to 512pt), window icons and the about box. At 64px it was
// scaled up there and blurred.
//
//go:embed icon-1024.png
var appIconLarge []byte

// appIconFor is the icon the app hands the system: none on a Mac whose
// bundle has AppIcon.icon compiled in (Assets.car), whose Dock then draws
// it light or dark as macOS 26 has it, where a picture set at run time
// stays light, a white tile in a dark Dock (#117).
func appIconFor() []byte {
	if runtime.GOOS == "darwin" {
		if exe, err := os.Executable(); err == nil {
			if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "..", "Resources", "Assets.car")); err == nil {
				return nil
			}
		}
	}
	return appIconLarge
}

type host struct {
	app   *application.App
	panel *application.WebviewWindow
	main  *application.WebviewWindow
	tray  *application.SystemTray
	// flapping is set while the tray bird plays its flap, so a second
	// click in it doesn't start another over it
	flapping atomic.Bool

	panelHeight int
	glides      atomic.Int64 // the newest panel glide; older ones stop
	query       string       // what the windows' URLs carry (a forced theme)

	ready     chan struct{} // closed once the main window can be shown
	readyOnce sync.Once
}

// whenReady runs fn once the main window can be shown safely.
func (h *host) whenReady(fn func()) {
	go func() {
		<-h.ready
		fn()
	}()
}

func (h *host) HidePanel() { h.panel.Hide() }
func (h *host) ShowMain(view string) {
	h.panel.Hide()
	if view != "" {
		h.main.SetURL("/?view=" + view + h.query)
	}
	h.dock(settings.Load(), true)
	h.main.Show()
	h.main.Focus()
}

// Import opens the window on an import link, for the user to confirm.
func (h *host) Import(link string) {
	id := stash(link)
	h.whenReady(func() {
		h.panel.Hide()
		h.main.SetURL("/?view=providers&import=" + id + h.query)
		h.dock(settings.Load(), true)
		h.main.Show()
		h.main.Focus()
	})
}

// dock puts magpie in the Dock or takes it out as s says, with the window
// shown or not: always, never, or while the window is.
func (h *host) dock(s settings.Settings, shown bool) {
	setDock(s.Dock || s.DockWindow && shown, shown)
}

func (h *host) Quit()                        { h.app.Quit() }
func (h *host) OpenURL(url string)           { _ = h.app.Browser.OpenURL(url) }
func (h *host) OpenFolder(path string) error { return openFolder(h.app, path) }
func (h *host) Copy(text string) bool        { return h.app.Clipboard.SetText(text) }
func (h *host) ChooseFolder(title string) (string, error) {
	return h.app.Dialog.OpenFile().CanChooseDirectories(true).CanChooseFiles(false).CanCreateDirectories(true).
		SetTitle(title).AttachToWindow(h.main).PromptForSingleSelection()
}

// panelMax keeps the panel a drop-down, not most of the screen: longer
// content (the usage of many accounts) scrolls in it (#124)
const panelWidth, panelMin, panelMax = 440, 220, 560

// FitPanel grows or shrinks the panel to its content and keeps it anchored
// under the tray icon; a shown panel glides there when g says how.
func (h *host) FitPanel(height int, g Glide) {
	height = max(panelMin, min(panelMax, height))
	if h.panelHeight == height {
		return
	}
	h.panelHeight = height
	if h.panel.IsVisible() && h.glidePanel(height, g) {
		return
	}
	h.glides.Add(1)
	h.panel.SetSize(panelWidth, height)
	if h.panel.IsVisible() {
		_ = h.tray.PositionWindow(h.panel, 6)
	}
}

// Run starts the desktop app: a menu bar icon whose click drops down a compact
// panel, plus a regular window for when you want it to stay around.
// showMain opens the window immediately; otherwise only the tray icon appears.
// link is a magpie:// link the app was started with, to confirm and import.
func Run(version string, showMain bool, link string) error {
	Version = version
	// `make dev` runs the backend on its own, so a Go change restarts only
	// that, behind windows that stay up.
	if devRole() == "backend" {
		return devBackend(func(w Windows) http.Handler { return Handler(w, startBackend()) })
	}
	// After an update off the Mac, the old process starts this one and then
	// quits; let it go before looking for the gateway.
	update.AwaitPredecessor()
	go func() {
		if err := registerScheme(); err != nil {
			log.Println("magpie:// links:", err)
		}
		// Windows has no installer to put magpie in the Start menu
		shortcut.Ensure()
	}()
	// MAGPIE_THEME=light|dark forces the palette; handy for screenshots.
	theme := ""
	if t := os.Getenv("MAGPIE_THEME"); t != "" {
		theme = "&theme=" + t
	}
	h := &host{query: theme, ready: make(chan struct{})}
	go stats.Run(version, "app")
	handler := devShell(h)
	if handler == nil {
		handler = Handler(h, startBackend())
	}
	h.app = application.New(application.Options{
		// Windows and Linux start a new process for a magpie:// link (or a
		// second launch); it hands its arguments to the running one and quits.
		// The Mac sends the link to the running app itself.
		SingleInstance: singleInstance(h),
		Name:           "magpie",
		Description:    "one place to pick every agent's model",
		Icon:           appIconFor(),
		Assets:         application.AssetOptions{Handler: handler},
		Mac:            application.MacOptions{ActivationPolicy: dockPolicy(settings.Load().Dock)},
		Windows:        application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
		// A version downloaded but not restarted into is installed on the
		// way out, so the next launch is the new one.
		OnShutdown: func() { updates.install(false) },
		// Wails exits on some webview errors; say why before it does.
		ErrorHandler: func(err error) { log.Println("magpie:", err) },
	})

	onDock = func(s settings.Settings) { h.dock(s, h.main.IsVisible()) }
	// The Dock icon opens the window. Wails would show every hidden window
	// on it, the panel too, so the hook answers first and stops it.
	h.app.Event.RegisterApplicationEventHook(events.Mac.ApplicationShouldHandleReopen, func(e *application.ApplicationEvent) {
		h.ShowMain("")
		e.Cancel()
	})

	h.panel = h.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:            "panel",
		Title:           "magpie",
		URL:             "/?mode=panel" + theme,
		Width:           panelWidth,
		Height:          520,
		Hidden:          true,
		Frameless:       true,
		AlwaysOnTop:     true,
		DisableResize:   true,
		HideOnEscape:    true,
		HideOnFocusLost: true,
		BackgroundType:  application.BackgroundTypeTranslucent,
		Mac: application.MacWindow{
			Backdrop:     application.MacBackdropTranslucent,
			CornerRadius: 12,
		},
		Windows: application.WindowsWindow{HiddenOnTaskbar: true},
	})

	// the window opens at the size it was last given
	width, height := 660, 600
	if s := settings.Load().Window; len(s) == 2 && s[0] >= 560 && s[1] >= 420 {
		width, height = s[0], s[1]
	}
	h.main = h.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     "magpie",
		URL:       "/?" + theme,
		Width:     width,
		Height:    height,
		MinWidth:  560,
		MinHeight: 420,
		Hidden:    true,
		Mac: application.MacWindow{
			// no InvisibleTitleBarHeight: that strip drags from anywhere in
			// it, tabs included; the header marks what drags instead
			TitleBar: application.MacTitleBarHiddenInset,
		},
	})
	// A resize is kept once it settles; a maximised or full-screen window
	// is the screen's size, not one the user gave it.
	var resized *time.Timer
	h.main.OnWindowEvent(events.Common.WindowDidResize, func(*application.WindowEvent) {
		if resized != nil {
			resized.Stop()
		}
		resized = time.AfterFunc(500*time.Millisecond, func() {
			if h.main.IsMaximised() || h.main.IsFullscreen() || h.main.IsMinimised() {
				return
			}
			w, ht := h.main.Size()
			if w < 560 || ht < 420 {
				return
			}
			// macOS reports a window a pixel short of the size it was
			// opened at; kept as it is, the window would shrink a pixel at
			// every start
			s := settings.Load()
			if len(s.Window) == 2 && abs(s.Window[0]-w) <= 2 && abs(s.Window[1]-ht) <= 2 {
				return
			}
			s.Window = []int{w, ht}
			settings.Save(s)
		})
	})
	// Closing the window keeps the tray alive; quitting is a menu action.
	h.main.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		h.main.Hide()
		h.dock(settings.Load(), false)
		e.Cancel()
	})

	menu := h.app.NewMenu()
	menu.Add("Open magpie").OnClick(func(*application.Context) { h.ShowMain("") })
	menu.AddSeparator()
	menu.Add("Version " + version).SetEnabled(false)
	restart := menu.Add("Restart to Update").SetHidden(true)
	restart.OnClick(func(*application.Context) {
		if restartToUpdate(false) {
			h.app.Quit()
		}
	})
	menu.Add("Quit magpie").OnClick(func(*application.Context) { h.app.Quit() })
	updates.onReady = func(v string) {
		application.InvokeSync(func() {
			restart.SetLabel("Restart to Update to " + v).SetHidden(false)
			menu.Update()
		})
	}
	updates.start()
	// the library written into the agents again, once: one installed or
	// updated since (or an edit by hand) gets it without a visit to the page
	go func() {
		if res, err := library.Sync(); err != nil {
			log.Println("library sync:", err)
		} else {
			for _, p := range res.Problems {
				log.Println("library sync:", p.Agent, p.What, p.Error)
			}
		}
	}()

	h.tray = h.app.SystemTray.New()
	h.tray.SetTooltip("magpie")
	if runtime.GOOS == "darwin" {
		h.tray.SetTemplateIcon(trayIcon)
	} else {
		h.tray.SetIcon(appIcon)
	}
	h.tray.SetMenu(menu)
	h.tray.AttachWindow(h.panel).WindowOffset(6)
	h.watchTrayUsage()
	// the quick panel by the icon, or the main window if the user would
	// rather (Settings → Tray icon)
	h.tray.OnClick(func() {
		if runtime.GOOS == "darwin" {
			go h.flap()
		}
		if settings.Load().Tray == "window" {
			h.ShowMain("")
			return
		}
		h.tray.ToggleWindow()
	})

	// Wails shows a Windows webview 3s after Show whether or not WebView2
	// has made its controller yet, and a slow first start then crashes on
	// the nil controller; there, wait for the first page.
	markReady := func() { h.readyOnce.Do(func() { close(h.ready) }) }
	if runtime.GOOS == "windows" {
		h.main.OnWindowEvent(events.Windows.WebViewNavigationCompleted, func(*application.WindowEvent) { markReady() })
	} else {
		h.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
			plainTitlebar(h.main) // Linux: the page's header is the title bar
			markReady()
		})
	}
	if showMain {
		h.whenReady(func() { h.ShowMain("") })
	}
	if link != "" {
		h.Import(link)
	}
	// Windows and Linux also report the start's own link as an event.
	var skip sync.Once
	h.app.Event.OnApplicationEvent(events.Common.ApplicationLaunchedWithUrl, func(e *application.ApplicationEvent) {
		u := ImportLink([]string{e.Context().URL()})
		dup := false
		if u == link {
			skip.Do(func() { dup = true })
		}
		if u != "" && !dup {
			h.Import(u)
		}
	})
	return h.app.Run()
}

// singleInstance makes a second launch hand over to this one, off the Mac.
// The id covers the executable and the config dir, so a build elsewhere or
// a sandboxed HOME runs on its own.
func singleInstance(h *host) *application.SingleInstanceOptions {
	if runtime.GOOS == "darwin" || !sessionBus() {
		return nil
	}
	exe, _ := os.Executable()
	sum := sha256.Sum256([]byte(exe + "\x00" + settings.Dir()))
	return &application.SingleInstanceOptions{
		UniqueID: "ai.usemagpie.app.i" + hex.EncodeToString(sum[:6]),
		OnSecondInstanceLaunch: func(d application.SecondInstanceData) {
			args := d.Args
			if len(args) > 0 {
				args = args[1:]
			}
			switch {
			case ImportLink(args) != "":
				h.Import(ImportLink(args))
			case len(args) == 1 && args[0] == "tray":
			default:
				h.whenReady(func() { h.ShowMain("") })
			}
		},
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// flap plays trayFlap on the tray icon, a frame every 30ms as they were
// drawn — the .9s of the header logo's flap — and ends on the still bird.
func (h *host) flap() {
	if !h.flapping.CompareAndSwap(false, true) {
		return
	}
	defer h.flapping.Store(false)
	names, _ := fs.Glob(trayFlap, "trayflap/*.png")
	for _, n := range names {
		b, err := trayFlap.ReadFile(n)
		if err != nil {
			break
		}
		h.tray.SetTemplateIcon(b)
		time.Sleep(30 * time.Millisecond)
	}
	h.tray.SetTemplateIcon(trayIcon)
}
