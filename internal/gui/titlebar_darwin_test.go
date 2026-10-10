//go:build darwin && cgo && !nogui

package gui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"testing"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/yetone/magpie/internal/gui/windowshape"
)

// titlebarRun is what the separate process measured: the main window, one
// with a plain title bar and one with the toolbar (the main window's as it
// was), at each stage, the main window and the toolbar one as AppKit draws
// a new system appearance, how many window drags a first press Y points
// below the top of each began, and how far below the top a sheet on each
// begins.
type titlebarRun struct {
	Stages []struct {
		Name                 string
		Main, Plain, Toolbar windowshape.Shape
	}
	Drawn   struct{ Main, Toolbar windowshape.Shape }
	Presses []struct {
		Y                    float64
		Main, Plain, Toolbar int
	}
	Sheet struct{ Main, Plain, Toolbar float64 }
}

const titlebarMark = "titlebar run: "

// The macOS SDKs a test runs the windows' program as linked against
// (runAppKitSDK): AppKit gives one linked against macOS 26's or a later one
// the design of macOS 26.
const (
	sdkMacOS15 uint32 = 15 << 16
	sdkMacOS26 uint32 = 26 << 16
)

// The main window has the corners of a window with a plain title bar
// (TextEdit's, an Electron app's), not a toolbar's: in the design of macOS
// 26 one with a toolbar has larger ones. Its traffic lights, its title bar's
// height and where a sheet on it begins stay where the toolbar had them,
// also after a new title and size and a new system appearance. The lights
// are in place as AppKit draws that appearance, too. A first press while
// another app is in front moves it from where it did with the toolbar. In
// the earlier design, where a toolbar doesn't change the corners (an older
// macOS, or a program linked against an earlier SDK than macOS 26's), the
// window keeps its toolbar. The windows' program is run as linked against
// each SDK, whatever the toolchain has, in a process of its own that runs
// AppKit on its main thread.
func TestMainWindowCorners(t *testing.T) {
	for _, sdk := range []struct {
		name string
		v    uint32
	}{{"macOS 26 SDK", sdkMacOS26}, {"macOS 15 SDK", sdkMacOS15}} {
		t.Run(sdk.name, func(t *testing.T) { checkMainWindowCorners(t, sdk.v) })
	}
}

func checkMainWindowCorners(t *testing.T, sdk uint32) {
	var run titlebarRun
	measureWindows(t, sdk, "corners", &run)
	near := func(a, b float64) bool { return math.Abs(a-b) < 0.5 }
	for _, s := range run.Stages {
		t.Logf("%s: corners %.2fpt (plain %.2f, toolbar %.2f), lights %v (toolbar %v), title bar %.0fpt (toolbar %.0f), toolbar kept %v", s.Name,
			s.Main.Radius, s.Plain.Radius, s.Toolbar.Radius, s.Main.Lights, s.Toolbar.Lights, s.Main.Titlebar, s.Toolbar.Titlebar, s.Main.Toolbar)
		if s.Plain.Radius < 0 {
			t.Errorf("%s: AppKit doesn't tell a window's corner radius", s.Name)
		} else if !near(s.Main.Radius, s.Plain.Radius) {
			t.Errorf("%s: corners of %.2fpt; a window with a plain title bar has %.2fpt, one with a toolbar %.2fpt",
				s.Name, s.Main.Radius, s.Plain.Radius, s.Toolbar.Radius)
		}
		for l := range 3 {
			if m, tb := s.Main.Lights[l], s.Toolbar.Lights[l]; !near(m[0], tb[0]) || !near(m[1], tb[1]) {
				t.Errorf("%s: traffic light %d at %v (x, y from the top left), the toolbar had it at %v", s.Name, l+1, m, tb)
			}
		}
		if !near(s.Main.Titlebar, s.Toolbar.Titlebar) {
			t.Errorf("%s: a title bar %.0fpt tall, the toolbar's was %.0fpt", s.Name, s.Main.Titlebar, s.Toolbar.Titlebar)
		}
		// Where a toolbar doesn't change the corners (the earlier design),
		// the main window is left as it was
		if near(s.Plain.Radius, s.Toolbar.Radius) && !s.Main.Toolbar {
			t.Errorf("%s: a toolbar doesn't change the corners in this design, yet the main window's was taken off", s.Name)
		}
	}
	if len(run.Stages) != 3 {
		t.Errorf("measured %d stages, want 3", len(run.Stages))
	}
	t.Logf("as AppKit draws a new system appearance: lights %v (toolbar %v), title bar %.0fpt", run.Drawn.Main.Lights,
		run.Drawn.Toolbar.Lights, run.Drawn.Main.Titlebar)
	for l := range 3 {
		if m, tb := run.Drawn.Main.Lights[l], run.Drawn.Toolbar.Lights[l]; !near(m[0], tb[0]) || !near(m[1], tb[1]) {
			t.Errorf("as AppKit draws a new system appearance, traffic light %d is at %v (x, y from the top left); the toolbar had it at %v",
				l+1, m, tb)
		}
	}
	for _, p := range run.Presses {
		t.Logf("a first press %.0fpt below the top, another app in front: %d window drags (plain %d, toolbar %d)", p.Y, p.Main, p.Plain, p.Toolbar)
		if p.Main < 0 || p.Plain < 0 || p.Toolbar < 0 {
			t.Errorf("a window was the key one, so a press as when another app is in front couldn't be tried")
		} else if p.Main != p.Toolbar {
			t.Errorf("a first press %.0fpt below the top, with another app in front, began %d window drags; with the toolbar it began %d (with a plain title bar: %d)",
				p.Y, p.Main, p.Toolbar, p.Plain)
		}
	}
	// AppKit moves each of them from the top of its title bar: a press there
	// that moved none didn't arrive
	if len(run.Presses) != 4 || run.Presses[0].Plain != 1 || run.Presses[0].Toolbar != 1 {
		t.Errorf("a first press near the top didn't begin a window drag, or not each was tried: %+v", run.Presses)
	}
	t.Logf("a sheet begins %.0fpt below the top (plain %.0f, toolbar %.0f)", run.Sheet.Main, run.Sheet.Plain, run.Sheet.Toolbar)
	if !near(run.Sheet.Main, run.Sheet.Toolbar) {
		t.Errorf("a sheet on a short window begins %.0fpt below its top; below the toolbar it began %.0fpt (with a plain title bar: %.0fpt)",
			run.Sheet.Main, run.Sheet.Toolbar, run.Sheet.Plain)
	}
}

// With its traffic lights on the right, as in a right-to-left language,
// the main window keeps its toolbar, also in the design of macOS 26: the
// lights are kept in place from the left.
func TestMainWindowRightToLeft(t *testing.T) {
	var s windowshape.Shape
	// as Xcode launches an app in its right-to-left pseudolanguage
	measureWindows(t, sdkMacOS26, "right to left", &s,
		"-AppleTextDirection", "YES", "-NSForceRightToLeftWritingDirection", "YES")
	t.Logf("lights %v (x, y from the top left), toolbar kept %v", s.Lights, s.Toolbar)
	if s.Lights[0][0] < s.Lights[2][0] {
		t.Fatalf("launched right to left, the traffic lights are on the left: %v", s.Lights)
	}
	if !s.Toolbar {
		t.Errorf("the traffic lights are on the right, and the main window's toolbar was taken off")
	}
}

// measureWindows measures the windows in mode in a process of its own, run
// as linked against the macOS SDK sdk (runAppKitSDK), with args for AppKit,
// and decodes what it measured into v.
func measureWindows(t *testing.T, sdk uint32, mode string, v any, args ...string) {
	t.Helper()
	out, err := runAppKitSDK(t, sdk, 40*time.Second, []string{"MAGPIE_TEST_TITLEBAR=" + mode}, args...)
	i := bytes.LastIndex(out, []byte(titlebarMark))
	if err != nil || i < 0 {
		t.Fatalf("measuring the windows: %v\n%s", err, out)
	}
	line, _, _ := bytes.Cut(out[i+len(titlebarMark):], []byte("\n"))
	if err := json.Unmarshal(line, v); err != nil {
		t.Fatalf("%v: %s", err, line)
	}
}

// The process measureWindows runs: the arguments go to AppKit, as the
// test's own flags are never parsed.
func init() {
	mode := os.Getenv("MAGPIE_TEST_TITLEBAR")
	if mode == "" {
		return
	}
	app := application.New(application.Options{
		Name: "magpie title bar regression",
		Mac:  application.MacOptions{ActivationPolicy: application.ActivationPolicyProhibited},
		Assets: application.AssetOptions{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<body style="background:#f4f1ea">`)
		})},
	})
	h := &host{app: app}
	main := h.makeMain("/")
	if mode == "right to left" {
		app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
			plainTitlebar(main)
			go func() {
				application.InvokeSync(func() { main.SetSize(900, 600); main.Show() })
				time.Sleep(time.Second)
				var s windowshape.Shape
				application.InvokeSync(func() { s = windowshape.Of(main.NativeWindow()) })
				measured(app, s)
			}()
		})
		runAndExit(app)
	}
	ref := func(tb application.MacTitleBar) *application.WebviewWindow {
		m := mainMacWindow()
		m.TitleBar = tb
		return app.Window.NewWithOptions(application.WebviewWindowOptions{URL: "/", Width: 900, Height: 600, Hidden: true, Mac: m})
	}
	wins := []*application.WebviewWindow{main, ref(application.MacTitleBarHidden), ref(application.MacTitleBarHiddenInset)}
	started := make(chan struct{})
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		plainTitlebar(main) // as Run does, before the window is first shown
		close(started)
	})
	each := func(f func(i int, w *application.WebviewWindow)) {
		application.InvokeSync(func() {
			for i, w := range wins {
				f(i, w)
			}
		})
	}
	go func() {
		<-started
		var run titlebarRun
		measure := func(name string) {
			time.Sleep(time.Second)
			var s [3]windowshape.Shape
			each(func(i int, w *application.WebviewWindow) { s[i] = windowshape.Of(w.NativeWindow()) })
			run.Stages = append(run.Stages, struct {
				Name                 string
				Main, Plain, Toolbar windowshape.Shape
			}{name, s[0], s[1], s[2]})
		}
		each(func(_ int, w *application.WebviewWindow) { w.SetSize(900, 600); w.Show() })
		measure("shown")
		each(func(_ int, w *application.WebviewWindow) { w.SetTitle("magpie, renamed"); w.SetSize(1000, 680) })
		measure("a new title and size")
		// AppKit draws a new system appearance before the run loop turns:
		// the windows are measured at once, and again once it has
		application.InvokeSync(func() {
			windowshape.FlipIncreaseContrast()
			run.Drawn.Main, run.Drawn.Toolbar = windowshape.Of(main.NativeWindow()), windowshape.Of(wins[2].NativeWindow())
		})
		measure("a new system appearance")
		// The app may not activate, so the windows are never the key one
		for _, y := range []float64{20, 40, 60, 70} {
			var d [3]int
			each(func(i int, w *application.WebviewWindow) { d[i] = windowshape.FirstPress(w.NativeWindow(), 200, y) })
			run.Presses = append(run.Presses, struct {
				Y                    float64
				Main, Plain, Toolbar int
			}{y, d[0], d[1], d[2]})
		}
		// macOS 26 centres a sheet, no higher than where it may begin: in a
		// short window, that's where it is
		each(func(_ int, w *application.WebviewWindow) { w.SetSize(900, 480) })
		time.Sleep(time.Second)
		var sheets [3]unsafe.Pointer
		each(func(i int, w *application.WebviewWindow) { sheets[i] = windowshape.BeginSheet(w.NativeWindow(), 448) })
		time.Sleep(1500 * time.Millisecond)
		var tops [3]float64
		each(func(i int, w *application.WebviewWindow) {
			tops[i] = windowshape.SheetTop(w.NativeWindow(), sheets[i])
			windowshape.EndSheet(w.NativeWindow(), sheets[i])
		})
		run.Sheet.Main, run.Sheet.Plain, run.Sheet.Toolbar = tops[0], tops[1], tops[2]
		measured(app, run)
	}()
	runAndExit(app)
}

// measured prints what was measured, for measureWindows, and quits.
func measured(app *application.App, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	fmt.Println(titlebarMark + string(b))
	app.Quit()
}

func runAndExit(app *application.App) {
	if err := app.Run(); err != nil {
		panic(err)
	}
	os.Exit(0)
}
