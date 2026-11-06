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

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/autostart"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/omarchy"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/shortcut"
	"github.com/yetone/magpie/internal/stats"
	"github.com/yetone/magpie/internal/update"
	"github.com/yetone/magpie/internal/webview2env"
)

//go:embed tray.png
var trayIcon []byte // black glyph, tinted by the macOS menu bar

// trayFlap is the bird beating its wing and flicking its tail, played on
// the tray icon when it is clicked (build/icon/gen.go tray-flap).
//
//go:embed trayflap/*.png
var trayFlap embed.FS

// winTrayIcon is the tile for Windows' tray, at each size it asks for
// (build/icon/gen.go win-ico): filling its square as the icons beside it
// do, where the Mac's tile with its clear margin looked a size smaller.
//
//go:embed icon-windows.ico
var winTrayIcon []byte

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

	panelHeight int          // the panel's, in points
	panelPage   int          // what its page last asked for, in CSS pixels
	textSize    atomic.Int64 // Settings' text size, in percent (settings.TextSizes)
	hyprRoom    int          // how tall it may grow under Hyprland's bar; 0 elsewhere
	clicks      sync.Once    // Hyprland's clicks heard, to close the panel on one outside it
	glides      atomic.Int64 // the newest panel glide; older ones stop
	barTray     atomic.Int64 // the newest setTrayInBar; older ones stop
	query       string       // what the windows' URLs carry (a forced theme)

	// niri places the panel as it opens (#1670): niriWatch starts its
	// watch once, and iconX is where the tray icon was just clicked
	// across its bar
	niri      omarchy.NiriPanel
	niriWatch sync.Once
	iconX     atomic.Pointer[int]

	// closing is set while a full-screen main window, closed, leaves full
	// screen; it is hidden once it has
	closing atomic.Bool

	// quitAt is when a Quit the user chose last asked first (AskQuit, unix
	// nanoseconds)
	quitAt atomic.Int64

	ready     chan struct{} // closed once the main window can be shown
	readyOnce sync.Once

	// lightweight mode (#580): see lightweight.go
	winMu sync.Mutex
	gone  atomic.Pointer[application.WebviewWindow] // the main window let go
	// windows made again whose page hasn't come yet, shown by whenLoaded's
	// fn; on the main thread
	loading map[*application.WebviewWindow]bool
	// the main window placed as it was last left (placeMain), and so shown
	placed atomic.Pointer[application.WebviewWindow]

	// the quick panel's shortcut bound now, "" for none (panelkey_app.go)
	keyMu    sync.Mutex
	panelKey string
}

// whenReady runs fn once the main window can be shown safely.
func (h *host) whenReady(fn func()) {
	go func() {
		<-h.ready
		fn()
	}()
}

func (h *host) HidePanel() {
	application.InvokeSync(func() {
		hideWebview(h.panel)
	})
}
func (h *host) ShowMain(view string) {
	url := ""
	if view != "" {
		url = mainURL(view, h.query)
	}
	application.InvokeSync(func() { h.openMain(url) })
}

// MainShown says whether the window is up.
func (h *host) MainShown() bool { return application.InvokeSyncWithResult(h.mainShown) }

// Import opens the window on an import link, for the user to confirm.
func (h *host) Import(link string) {
	id := stash(link)
	h.whenReady(func() {
		application.InvokeSync(func() { h.openMain("/?view=providers&import=" + id + h.query) })
	})
}

// dock puts magpie in the Dock or takes it out as s says, with the window
// shown or not, full screen or not.
func (h *host) dock(s settings.Settings, shown bool) { h.dockFront(s, shown, shown) }

// dockFront is dock, with front saying whether joining the Dock activates
// magpie: not for the window a restart by itself brings back.
func (h *host) dockFront(s settings.Settings, shown, front bool) {
	full := application.InvokeSyncWithResult(func() bool { return h.main != nil && h.main.IsFullscreen() })
	setDock(inDock(s, shown, full), front)
}

// inDock: always, never, or while the window is shown (the default), as s
// says, and while it is full screen whatever s says (dockOnFullscreen).
func inDock(s settings.Settings, shown, fullscreen bool) bool {
	return s.Dock || !s.DockNever && shown || fullscreen
}

func (h *host) Quit() { h.app.Quit() }
func (h *host) OpenURL(url string) error {
	err := h.app.Browser.OpenURL(url)
	if err == nil {
		return nil
	}
	// Wails starts Windows' rundll32 by its bare name, which a PATHEXT
	// without .EXE can't find (#2043); proc names it by its path
	if proc.OpenBrowser(url) == nil {
		return nil
	}
	return err
}
func (h *host) OpenFolder(path string) error   { return openFolder(h.app, path) }
func (h *host) RevealFolder(path string) error { return revealFolder(h.app, path) }
func (h *host) Copy(text string) bool          { return copyText(h.app, text) }
func (h *host) ChooseFolder(title string) (string, error) {
	d := h.app.Dialog.OpenFile().CanChooseDirectories(true).CanChooseFiles(false).CanCreateDirectories(true).SetTitle(title)
	if w := application.InvokeSyncWithResult(func() *application.WebviewWindow { return h.main }); w != nil {
		d.AttachToWindow(w)
	}
	return d.PromptForSingleSelection()
}

// panelTitle names the panel for Hyprland's rule, which places it, and
// for niri, which finds it by it (the main window is "magpie")
const panelTitle = "magpie panel"

// FitPanel grows or shrinks the panel to its content and keeps it anchored
// under the tray icon; a shown panel glides there when g says how. height
// is the page's, in its CSS pixels: at a larger text size the panel is
// that much taller (and wider), no taller than the screen has room for.
func (h *host) FitPanel(height int, g Glide) {
	application.InvokeSync(func() { h.fitPanel(height, g) })
}

func (h *host) fitPanel(height int, g Glide) {
	h.panelPage = height
	_, height = panelFrame(height, h.zoom(), h.panelRoom())
	if h.panelHeight == height {
		return
	}
	h.panelHeight = height
	if h.panel == nil { // let go: made again at this height
		return
	}
	if windowOpen(h.panel) && h.glidePanel(height, g) {
		return
	}
	h.glides.Add(1)
	h.panel.SetSize(h.panelW(), height)
	if windowOpen(h.panel) {
		_ = h.tray.PositionWindow(h.panel, 6)
	}
}

// zoom is the text size the pages are drawn at, as a factor.
func (h *host) zoom() float64 { return zoomOf(int(h.textSize.Load())) }

// panelW is the panel's width, in points, at the text size.
func (h *host) panelW() int { return zoomed(panelWidth, h.zoom()) }

// panelRoom is how tall the panel can be on its screen: the work area,
// less the gap under the menu bar (or over the taskbar) and a little air.
func (h *host) panelRoom() int {
	if h.hyprRoom > 0 { // under Hyprland's bar, down to the work area's foot
		return h.hyprRoom
	}
	if _, rh := screenRoom(h.panel); rh > 0 {
		return rh - 16
	}
	return 0
}

// screenRoom is the work area of the screen w is on, 0s when unknown.
func screenRoom(w *application.WebviewWindow) (int, int) {
	if w == nil {
		return 0, 0
	}
	s, err := w.GetScreen()
	if err != nil || s == nil {
		return 0, 0
	}
	return s.WorkArea.Width, s.WorkArea.Height
}

// SetTextSize zooms both pages to percent, the Settings page's choice or a
// Ctrl/Cmd +, − or 0 in either window. The panel is resized at once, to
// the height its page last asked for; the main window is kept no smaller
// than the page's least, larger if it has to be.
func (h *host) SetTextSize(percent int) {
	if int(h.textSize.Swap(int64(percent))) == percent {
		return
	}
	h.whenReady(h.applyZoom)
}

// applyZoom puts the text size on the windows as they are now; one
// lightweight mode let go is made again at it.
func (h *host) applyZoom() { application.InvokeSync(h.zoomWindows) }

func (h *host) zoomWindows() {
	z := h.zoom()
	if h.main != nil {
		setPageZoom(h.main, z)
		sw, sh := screenRoom(h.main)
		h.main.SetMinSize(windowMin(z, sw, sh))
	}
	page := h.panelPage
	if page == 0 {
		page = panelStart
	}
	w, ht := panelFrame(page, z, h.panelRoom())
	h.glides.Add(1)
	h.panelHeight = ht
	if h.panel == nil {
		return
	}
	setPageZoom(h.panel, z)
	h.panel.SetSize(w, ht)
	if windowOpen(h.panel) {
		_ = h.tray.PositionWindow(h.panel, 6)
	}
}

// windowsOptions are the app's Windows options. Portable, WebView2 keeps
// its profile in data\webview2: left to Wails it is %APPDATA%\magpie.exe,
// a folder a portable magpie must not leave behind (#508). Installed, it
// stays there, so what the pages kept isn't lost. The browser arguments
// the user set in WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS, which Wails' loader
// blanks before WebView2 can read them, are merged into Wails' own (#2753).
// Display changes are read with a callback made once (screens_windows.go).
func windowsOptions() application.WindowsOptions {
	disabled, enabled, other := webView2Args(webview2env.Args)
	return application.WindowsOptions{
		DisableQuitOnLastWindowClosed: true,
		WebviewUserDataPath:           appdir.WebView(),
		DisabledFeatures:              disabled,
		EnabledFeatures:               enabled,
		AdditionalBrowserArgs:         other,
		WndProcInterceptor:            screensWndProc,
	}
}

// panelStart is the panel's height before its page first asks for one.
const panelStart = 520

// Run starts the desktop app: a menu bar icon whose click drops down a compact
// panel, plus a regular window for when you want it to stay around.
// showMain opens the window immediately; otherwise only the tray icon appears.
// link is a magpie:// link the app was started with, to confirm and import.
func Run(version string, showMain bool, link string) error {
	Version = version
	webkitDefaults()
	// `make dev` runs the backend on its own, so a Go change restarts only
	// that, behind windows that stay up.
	if devRole() == "backend" {
		return devBackend(func(w Windows) http.Handler { return Handler(w, startBackend()) })
	}
	// Windows: the app opened from Downloads (or the Desktop, or a zip)
	// opens the copy installed in the user's programs folder, putting
	// itself there first, and Open at login opens that one from now on.
	// The first time, it asks where (installdir_windows.go).
	askInstallFolder()
	if from, to := update.Settle(); to != "" {
		err := update.Forward(from, to)
		if err == nil {
			if err := autostart.Repoint(from, to); err != nil {
				log.Println("open at login:", err)
			}
			return nil
		}
		log.Println("install:", err)
	}
	// A restart to update on a Mac or Linux started this one beside the
	// magpie before, which hands it the gateway (handover.go).
	startExe, _ = update.Executable()
	handoverHost.Store(true)
	before := 0
	if startExe != "" {
		before = takeOver(startExe)
	}
	// After an update off the Mac, the old process starts this one and then
	// quits; let it go before looking for the gateway.
	update.AwaitPredecessor()
	// installed from a download just now: what magpie left beside it goes
	update.Settled()
	// the Mac's second launch hands over to the magpie already running
	if runningAlready(showMain || OpenPanel, link) {
		return nil
	}
	// a restart magpie made by itself: back as it was, taking no focus
	// (quiet_restart.go)
	quiet := takeQuietRestart()
	quietShow.Store(quiet && showMain)
	// before the first webview, which opens WebKit's data where programName says
	keepWebData()
	go func() {
		if err := registerScheme(); err != nil {
			log.Println("magpie:// links:", err)
		}
		// Windows has no installer to put magpie in the Start menu
		shortcut.Ensure()
		// Open at login as this version writes it (the Mac's, so a restart
		// to update from a magpie opened at login comes back)
		if err := autostart.Refresh(); err != nil {
			log.Println("open at login:", err)
		}
	}()
	// MAGPIE_THEME=light|dark forces the palette; handy for screenshots.
	theme := ""
	if t := os.Getenv("MAGPIE_THEME"); t != "" {
		theme = "&theme=" + t
	}
	h := &host{query: theme, ready: make(chan struct{})}
	// the pages open at the saved text size: Windows' and Linux's webviews
	// take it as they are made (the Zoom options below), the Mac's once
	// the app has started (applyZoom)
	h.textSize.Store(int64(settings.Load().TextSize))
	zoom := h.zoom()
	go stats.Run(version, "app")
	handler := devShell(h)
	ownPage := handler == nil
	var gw *gateway.Server
	if ownPage {
		gw = startBackend()
		handler = Handler(h, gw)
	}
	// Linux: the magpie before holds the app's D-Bus name until it quits,
	// once it has finished its requests; this one serves the gateway
	// meanwhile, and puts its tray icon up after
	if before != 0 && runtime.GOOS == "linux" {
		awaitExit(before, handedOffWait)
	}
	// a quiet start joins the Dock once started (below): Wails activates
	// an app that starts in it
	policy := dockPolicy(settings.Load().Dock && !quiet)
	h.app = application.New(application.Options{
		// Windows and Linux start a new process for a magpie:// link (or a
		// second launch); it hands its arguments to the running one and quits.
		// The Mac sends the link to the running app itself.
		SingleInstance: singleInstance(h),
		Name:           "magpie",
		Description:    "one place to pick every agent's model",
		Icon:           appIconFor(),
		Assets:         application.AssetOptions{Handler: ownWindow(handler)},
		Mac:            application.MacOptions{ActivationPolicy: policy},
		Windows:        windowsOptions(),
		Linux:          linuxOptions(),
		// A version downloaded but not restarted into is installed on the
		// way out, so the next launch is the new one.
		// Quitting doesn't come back to main on a Mac (NSApp terminate:
		// exits), so the CLIs still being asked something end here.
		// Account failover magpie wrote into Codex goes with the gateway
		// this process serves: Codex left pointed at it would retry till
		// magpie is open again (Leon920717 on X). The next start writes it
		// again (Sync).
		// A magpie that handed the gateway to the new version (handover.go)
		// leaves both to it: the failover goes on through the new one, and
		// so do the threads.
		OnShutdown: func() {
			proc.EndProbes()
			if handedOver.Load() {
				return
			}
			if served.Load() != nil {
				ended, err := agent.EndCodexFailover()
				if err != nil {
					log.Println("codex failover:", err)
				}
				// the threads Codex has open keep the gateway's address
				// they began with: a relay serves them there on Codex's
				// own sign-in once the gateway has let go (codexrelay;
				// idkatbro on X: 503 "Unknown error" till magpie reopened)
				if ended {
					if err := agent.RelayOpenCodex(); err != nil {
						log.Println("codex relay:", err)
					}
				}
			}
			// the compactions magpie made in Codex's threads written as
			// Codex's own, for the threads no Codex has open: with magpie
			// gone they still go on (LukeMa on X)
			agent.PortCodexRollouts(3 * time.Second)
			updates.install(false)
		},
		// Wails exits on some webview errors; say why before it does.
		ErrorHandler: func(err error) { log.Println("magpie:", err) },
	})
	// Windows without the WebView2 runtime can make no window: Wails would
	// exit on the first, with nothing on screen to say why (#1806). The
	// reader is told in a box of Windows' own instead, and the launch ends.
	if !webView2Check(h.app) {
		return nil
	}
	var appQuit *application.MenuItem // the Mac's app menu's Quit
	// Wails' default Learn More replaces the current window with wails.io.
	// Keep the native menus, but open magpie's help in the system browser.
	// macOS only: it is the one that shows a menu bar unasked; on Linux a
	// set menu becomes a menu bar in every window, the panel's too.
	if runtime.GOOS == "darwin" {
		appMenu := application.DefaultApplicationMenu()
		help := appMenu.FindByRole(application.HelpMenu).GetSubmenu()
		help.Clear()
		help.Add("Learn More").OnClick(func(*application.Context) { _ = h.OpenURL("https://usemagpie.ai") })
		// Command-Q asks first while agents are connected (quitask.go). The
		// role's item sends terminate:, which logging out and shutting down
		// send too, so it is replaced rather than the app's ShouldQuit set
		appQuit = askingQuit(appMenu, trayText(trayLang(settings.Load().Lang, systemLang), "Quit magpie"), func() { go h.AskQuit() })
		h.app.Menu.Set(appMenu)
	}
	if Started != nil {
		h.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) { Started() })
	}

	onDock = func(s settings.Settings) { h.dock(s, h.MainShown()) }
	dockOnFullscreen()
	watchHide()
	// The Dock icon opens the window, or goes to it where it is open. Wails
	// would show every hidden window on it, the panel too, so the hook
	// answers first and stops it. The icon flaps as the header logo does.
	h.app.Event.RegisterApplicationEventHook(events.Mac.ApplicationShouldHandleReopen, func(e *application.ApplicationEvent) {
		go dockFlap()
		application.InvokeSync(h.reopenMain)
		e.Cancel()
	})

	h.panel = h.makePanel()
	h.main = h.makeMain("/?" + theme)
	onFonts = h.fontsChanged

	// the menu in the page's language, relabelled when that changes (#301)
	labels := trayMenuLabels(trayLang(settings.Load().Lang, systemLang), version, "")
	menu := h.app.NewMenu()
	open := menu.Add(labels.open).OnClick(func(*application.Context) { h.ShowMain("") })
	menu.AddSeparator()
	ver := menu.Add(labels.version).SetEnabled(false)
	restart := menu.Add(labels.restart).SetHidden(true)
	restart.OnClick(func(*application.Context) {
		// the window comes back if it was open; the tray alone if not
		now := func() bool {
			if restartToUpdate(false, h.MainShown(), "") {
				leave(h.app.Quit)
				return true
			}
			return false
		}
		updates.restartAsked(now)
	})
	quit := menu.Add(labels.quit).OnClick(func(*application.Context) { go h.AskQuit() })
	var ready string // the version waiting for a restart; on the main thread
	relabel := func() {
		l := trayMenuLabels(trayLang(settings.Load().Lang, systemLang), version, ready)
		if waiting, b := updates.waitingFor(); waiting && ready != "" {
			l.restart = trayRestartNow(trayLang(settings.Load().Lang, systemLang), b)
		}
		open.SetLabel(l.open)
		ver.SetLabel(l.version)
		restart.SetLabel(l.restart)
		quit.SetLabel(l.quit)
		menu.Update()
		if appQuit != nil {
			appQuit.SetLabel(l.quit)
		}
	}
	onLang = func() { application.InvokeSync(relabel) }
	updates.onReady = func(v string) {
		application.InvokeSync(func() {
			ready = v
			restart.SetHidden(false)
			relabel()
		})
	}
	updates.onWait = func() { application.InvokeSync(relabel) }
	// Settings' AutoRestart: the window comes back only if it was up (#2096)
	updates.autoRestart = func() bool {
		if restartInto(false, h.MainShown(), "", true) {
			leave(h.app.Quit)
			return true
		}
		return false
	}
	updates.start()
	news.start()
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
		// a set kept in a file of the user's (~/.agents/AGENTS.md) is
		// edited outside magpie: the agents get each change, until this
		// one has handed the gateway to the new version (#4938)
		library.KeepLinked(backendCtx, 30*time.Second)
	}()

	// before the item is made, which starts as hidden as AppKit kept it
	hiddenAtStart := bringTrayBack()
	h.tray = h.app.SystemTray.New()
	if runtime.GOOS == "linux" {
		// set before the tray starts, it is the item's id too, which
		// Omarchy's bar pins it by (Wails calls it "Wails" otherwise)
		h.tray.SetLabel("magpie")
		go dropTrayName()
	}
	h.tray.SetTooltip("magpie")
	if icon, template := trayIconFor(runtime.GOOS, traySchemeLight()); template {
		h.tray.SetTemplateIcon(icon)
	} else {
		h.tray.SetIcon(icon)
	}
	if runtime.GOOS == "linux" {
		go watchTrayScheme(func(light bool) {
			icon, _ := trayIconFor("linux", light)
			h.tray.SetIcon(icon)
		})
	}
	h.tray.SetMenu(menu)
	h.tray.AttachWindow(h.panel).WindowOffset(6)
	// before Wails removes the item as the app quits, which would forget
	// where the user put the icon (trayshown_darwin.go; nothing elsewhere)
	h.app.OnShutdown(func() { keepTrayPlace() })
	// a Mac that handed the gateway to the new version (handover.go)
	// leaves the menu bar to it while it finishes its requests: one bird
	if runtime.GOOS == "darwin" {
		onHandedOver = h.leaveMenuBar
		// a magpie before this fix hid its item for good as it handed
		// over (leaveMenuBar): once it has quit, bring the icon back
		if before != 0 && !hiddenAtStart {
			go func() {
				awaitExit(before, handedOffWait)
				<-h.ready
				if forgetTrayHidden() > 0 {
					log.Println("tray: brought back the menu bar icon the magpie before hid")
				}
				h.tray.Show()
			}()
		}
	}
	h.watchTrayUsage()
	h.startPanelKey(before)
	// Omarchy's bar shows magpie once: as its widget (Settings → Bar
	// icon) or as the tray's item, not both
	onBarIcon = h.setTrayInBar
	h.setTrayInBar(barIcon(h)["on"])
	go h.lighten()
	h.watchAlerts()
	// a double-click opens the main window where the tray tells one apart
	// (trayDoubleClicks: Windows); its first click opened the panel, which
	// openMain hides, and its last release is no click of its own
	var double trayDouble
	if trayDoubleClicks(runtime.GOOS) {
		h.tray.OnDoubleClick(func() {
			double.doubled(time.Now())
			h.ShowMain("")
		})
	}
	// the quick panel by the icon, or the main window if the user would
	// rather (Settings → Click the tray icon to)
	h.tray.OnClick(func() {
		if double.swallow(time.Now()) {
			return
		}
		if cmdClick() {
			return // Command-drag moves the icon; the system handles it
		}
		if runtime.GOOS == "linux" && menuOpening() {
			return // a right-click opening the menu, not a click (#1430)
		}
		if runtime.GOOS == "darwin" {
			go h.flap()
		}
		if settings.Load().Tray == "window" {
			h.ShowMain("")
			return
		}
		if x, ok := trayClick(h.tray); ok {
			h.iconX.Store(&x)
		}
		h.togglePanel()
	})

	// Wails shows a Windows webview 3s after Show whether or not WebView2
	// has made its controller yet, and a slow first start then crashes on
	// the nil controller; there, wait for the first page.
	markReady := func() { h.readyOnce.Do(func() { close(h.ready) }) }
	if runtime.GOOS == "windows" {
		h.main.OnWindowEvent(events.Windows.WebViewNavigationCompleted, func(*application.WindowEvent) { markReady() })
	} else {
		h.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
			plainTitlebar(h.main) // Linux: the page's header is the title bar; the Mac: a plain one's corners
			nameWindow(h.panel, panelTitle)
			ownFrame(h.panel) // KDE: no title bar of KWin's on it (#1283)
			markReady()
		})
	}
	// the Mac's webviews take the text size only once they are made;
	// Windows' took it with the options, and its panel's webview may not be
	// made yet here
	if zoom != 1 && runtime.GOOS != "windows" {
		h.whenReady(h.applyZoom)
	}
	if showMain {
		h.whenReady(func() { h.ShowMain(argView(OpenView)) })
	} else if quiet && settings.Load().Dock {
		// kept out of the Dock to start (Mac options above), it joins
		// now without coming to the front
		h.whenReady(func() { setDock(true, false) })
	}
	if OpenPanel {
		h.whenReady(func() { application.InvokeAsync(h.togglePanel) })
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
	// GTK 3 with no font DPI set would lay every page out at a negative
	// width (#1371); give it one before the first webview is made.
	fontDPI()
	// the page to a browser too, where Settings says (apppage.go)
	if ownPage {
		startAppPage(h, gw)
	}
	return h.app.Run()
}

// linuxOptions: the tray stays when lightweight mode has let both windows
// go, and the program name is the window's Wayland app_id and X11 WM_CLASS,
// which the desktop entry's StartupWMClass names (#2626).
func linuxOptions() application.LinuxOptions {
	return application.LinuxOptions{DisableQuitOnLastWindowClosed: true, ProgramName: programName}
}

// singleInstance makes a second launch hand over to this one, off the Mac.
// The id covers the config dir, so a sandboxed HOME runs on its own, but
// not the executable: two copies of magpie on one config (one autostarted
// from where it was first run, another from where it was put later) would
// share the gateway's port and put two icons in the tray.
func singleInstance(h *host) *application.SingleInstanceOptions {
	if runtime.GOOS == "darwin" || !sessionBus() {
		return nil
	}
	sum := sha256.Sum256([]byte(settings.Dir()))
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
			case len(args) == 1 && args[0] == "panel":
				h.whenReady(func() { application.InvokeAsync(h.togglePanel) })
			case len(args) == 2 && (args[0] == "gui" || args[0] == "app"):
				h.whenReady(func() { h.ShowMain(argView(args[1])) })
			default:
				h.whenReady(func() { h.ShowMain("") })
			}
		},
	}
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
		h.setBird(b)
		time.Sleep(30 * time.Millisecond)
	}
	h.setBird(trayIcon)
}

// setBird sets a frame of the bird: in the menu bar's image while it shows
// the usage cards (trayImageFrame), else as the tray's icon.
func (h *host) setBird(b []byte) {
	if !trayImageFrame(b) {
		h.tray.SetTemplateIcon(b)
	}
}

// panelOptions: the tray panel's window, frameless and (but on Linux)
// see-through. On
// Windows, Wails creates a frameless window as WS_OVERLAPPEDWINDOW with
// DWM's frame extended into the whole client area (for the shadow and the
// round corners), and with the close button left enabled (WS_SYSMENU) DWM
// draws the caption's close X into that extended frame at the top right,
// where it shows through the panel's translucent page over the Settings
// gear (#238). Hiding the caption buttons there takes WS_SYSMENU and the
// minimise and maximise boxes off, so DWM has nothing to draw; the shadow
// and corners stay. The Mac keeps what it had.
//
// On Linux the panel is a solid window, as the main window is (#1803). A
// see-through one is an RGBA, app-paintable GTK window under a transparent
// webview, and scrolling it left a fainter copy of the old position's
// paint behind (KDE Plasma 6, on Wayland and on XWayland, with WebKit's
// compositing mode on or off), while the same page in the main window
// never did. The page paints itself opaque there too (body.linux.panel in
// app.css).
func panelOptions(goos, theme string) application.WebviewWindowOptions {
	o := application.WebviewWindowOptions{
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
			// On every Space, a full-screen app's among them, as a menu
			// bar extra's panel is: by default (FullScreenPrimary alone)
			// it stayed on the desktop Space it was first shown on, and a
			// click on the icon over a full-screen Chrome showed it there,
			// out of sight, until the user swiped back (#2141).
			CollectionBehavior: application.MacWindowCollectionBehaviorCanJoinAllSpaces |
				application.MacWindowCollectionBehaviorFullScreenAuxiliary,
			// and shown without activating magpie, as a menu bar extra's
			// panel is: an ordinary window's Focus activated the app, and
			// the Mac then took the user to the Space of magpie's other
			// windows (the desktop the main window is on), off the
			// full-screen app's, panel and all (#2141, witt-bit, on
			// v0.1.1246). A non-activating panel still takes the keyboard.
			WindowClass:      application.MacWindowClassPanel,
			PanelPreferences: application.MacPanelPreferences{NonActivating: true},
		},
		Windows: application.WindowsWindow{HiddenOnTaskbar: true},
	}
	if goos == "linux" {
		o.BackgroundType = application.BackgroundTypeSolid
	}
	if goos == "windows" {
		o.MinimiseButtonState = application.ButtonHidden
		o.MaximiseButtonState = application.ButtonHidden
		o.CloseButtonState = application.ButtonHidden
	}
	return o
}

// OpenPanel has Run open the quick panel once it starts, as `magpie panel`
// does; a second `magpie panel` toggles it in the running one. Omarchy's bar
// icon runs it (see omarchy.AddWidget).
var OpenPanel bool

// OpenView is the tab the window opens on, as `magpie gui settings` asks:
// a restart to update comes back where it was asked for.
var OpenView string

// Started is called once the app has started, by when Wails handles SIGINT
// and SIGTERM itself (it starts listening as it runs, before the app is
// said to have started); until then a signal is magpie's to handle.
var Started func()

// leaveMenuBar is a Mac's magpie that handed the gateway to the new
// version leaving the menu bar, panel, window and Dock to it while it
// finishes its requests: one bird. Hidden, the item's visibility is kept by
// AppKit for the app, the new version's item and every later one too, so it
// is forgotten once the hide is made.
func (h *host) leaveMenuBar() {
	application.InvokeSync(func() {
		h.tray.Hide()
		forgetTrayHiddenLater()
		hideWebview(h.panel)
		hideWebview(h.main)
		setDock(false, false)
		// and its shortcut, for the new version to bind
		go h.dropPanelKey()
	})
}

// setTrayInBar has the tray's item leave the bar's tray while magpie's icon
// is in Omarchy's bar as its widget, and come back once it isn't. The item
// is there to be set a moment after the app has started; the newest call
// wins.
func (h *host) setTrayInBar(inBar bool) {
	gen := h.barTray.Add(1)
	go func() {
		<-h.ready
		for range 120 {
			if h.barTray.Load() != gen || setTrayPassive(h.tray, inBar) {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
}

// togglePanel opens the quick panel by the tray icon, or closes it.
func (h *host) togglePanel() { application.InvokeSync(h.togglePanelNow) }

func (h *host) togglePanelNow() {
	if h.loading[h.panel] {
		return // made again, it is shown once its page has come
	}
	if _, again := h.panelWin(); again {
		// made again, with its page to come: shown once it can be
		h.whenLoaded(h.panel, h.togglePanel)
		return
	}
	if omarchy.Hyprland() {
		if h.panel.IsVisible() {
			h.hidePanel()
			return
		}
		h.placePanel()
	}
	// niri places it as it opens (see omarchy.NiriPanel), by the click
	// that opened it, if a click did
	if x := h.iconX.Swap(nil); omarchy.Niri() && !h.panel.IsVisible() {
		h.niriWatch.Do(func() { go h.niri.Watch(panelTitle) })
		h.niri.Opening(x)
	}
	if runtime.GOOS == "darwin" {
		// Wails' ToggleWindow goes by IsVisible, which on the Mac is the
		// window's occlusion: under a window over the whole screen (a
		// remote-desktop server's, above every app's) the open panel read
		// as closed, and the icon showed it again instead of closing it
		// (#1661). Open is windowOpen: shown, covered or not.
		if windowOpen(h.panel) {
			hideWebview(h.panel)
			return
		}
		_ = h.tray.PositionWindow(h.panel, 6)
		showWebview(h.panel).Focus()
		h.tellPanelSide()
		return
	}
	h.tray.ToggleWindow()
	if h.panel.IsVisible() {
		h.tellPanelSide()
	}
}

// tellPanelSide tells the panel, just placed, which of its edges opens the
// main window: the one nearest the tray (panelOpenAt). Wayland doesn't say
// where the compositor put a window, so there it goes by the system.
func (h *host) tellPanelSide() {
	b := h.panel.Bounds()
	panel := panelRect{b.X, b.Y, b.Width, b.Height}
	var screen panelRect
	known := !waylandSession(runtime.GOOS, os.Getenv)
	if s, err := h.panel.GetScreen(); err == nil && s != nil {
		screen = panelRect{s.Bounds.X, s.Bounds.Y, s.Bounds.Width, s.Bounds.Height}
	} else {
		known = false
	}
	h.panel.ExecJS(panelOpenAtJS(panelOpenAt(runtime.GOOS, panel, screen, known)))
}

// placePanel puts the panel, about to open, under Hyprland's bar at the
// click, the way Omarchy's own drop-downs sit: a Wayland window can't place
// itself, so Hyprland is given a rule for it (see omarchy.PlacePanel).
func (h *host) placePanel() {
	mon, x, y, room, ok := omarchy.PanelAt(h.panelW())
	if !ok {
		return
	}
	if err := omarchy.PlacePanel(panelTitle, mon, x, y); err != nil {
		log.Println("hyprland:", err)
		return
	}
	h.hyprRoom = room
	h.clicks.Do(func() {
		go omarchy.WatchClicks(func() {
			switch {
			case !application.InvokeSyncWithResult(func() bool { return h.panel != nil && h.panel.IsVisible() }): // Escape closed it
				omarchy.StopClicks()
			case omarchy.ClickedOutside(panelTitle):
				application.InvokeAsync(h.hidePanel)
			}
		})
	})
	if err := omarchy.ReportClicks(); err != nil {
		log.Println("hyprland:", err)
	}
	if _, ht := h.panel.Size(); ht > room && room >= panelMin {
		h.panelHeight = room
		h.panel.SetSize(h.panelW(), room)
	}
}

// hidePanel closes the panel on Hyprland, where it has no focus-lost to
// close on (see omarchy.ReportClicks).
func (h *host) hidePanel() {
	hideWebview(h.panel)
	go omarchy.StopClicks()
}
