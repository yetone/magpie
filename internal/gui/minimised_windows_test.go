//go:build windows && !nogui

package gui

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/w32"
	"golang.org/x/sys/windows"

	"github.com/yetone/magpie/internal/settings"
)

// #3495 (Lemonawa): a restart by itself (Settings' AutoRestart) from a
// main window minimised on the taskbar brings it back minimised there: up
// (a later restart and lightweight mode count it so), never shown or made
// the foreground window on the way, and, restored from the taskbar, back
// at its size, maximised if it was. A window up and not minimised still
// comes back shown, as before.
//
// The windows are real, so this runs where Windows has a desktop the user
// sees: on the Windows box's signed-in session, not over ssh (session 0).
const testMinimisedEnv = "MAGPIE_TEST_MINIMISED"

func TestRestartMinimisedComesBackMinimised(t *testing.T) {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil || session == 0 {
		t.Skip("no interactive desktop (session 0): run it in the signed-in session")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"minimised", "minimised-maximised", "shown"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, self, "-test.run=^$")
			cmd.Env = append(os.Environ(), testMinimisedEnv+"="+mode, "XDG_CONFIG_HOME="+t.TempDir(), "LOCALAPPDATA="+t.TempDir())
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "minimised: ok") {
				t.Fatalf("%s: %v\n%s", mode, err, out)
			}
		})
	}
}

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	procIsIconic     = user32.NewProc("IsIconic")
	procPostMessageW = user32.NewProc("PostMessageW")
)

func init() {
	mode := os.Getenv(testMinimisedEnv)
	if mode == "" {
		return
	}
	fail := func(format string, a ...any) {
		fmt.Printf("minimised: "+format+"\n", a...)
		os.Exit(1)
	}
	if mode == "minimised-maximised" {
		s := settings.Load()
		s.WindowMaximised = true
		if err := settings.Save(s); err != nil {
			fail("settings: %v", err)
		}
	}
	// the page says every 50ms whether it is visible
	seen := make(chan string, 1024)
	app := application.New(application.Options{
		Name:    "magpie minimised restart regression",
		Windows: windowsOptions(),
		Assets: application.AssetOptions{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/vis" {
				select {
				case seen <- r.URL.Query().Get("s"):
				default:
				}
				return
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body>magpie<script>setInterval(() => fetch("/vis?s=" + document.visibilityState), 50)</script></body></html>`)
		})},
	})
	h := &host{app: app}
	onMain := func(f func() bool) (b bool) {
		application.InvokeSync(func() { b = f() })
		return
	}
	waitFor := func(what string, ok func() bool) {
		for deadline := time.Now().Add(15 * time.Second); !onMain(ok); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				fail("timed out waiting until %s", what)
			}
		}
	}
	// drawn: the window's webview is shown, its page visible, not a blank
	// webview (a controller left hidden tells its page it is hidden)
	drawn := func(when string) {
		deadline := time.After(10 * time.Second)
		for {
			select {
			case v := <-seen:
				if v == "visible" {
					return
				}
			case <-deadline:
				fail("%s: the page isn't visible", when)
			}
		}
	}
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			// what the old magpie left as it restarted by itself, read as a
			// start reads it (app.go)
			markQuietRestart(mode != "shown")
			quiet, minimised := takeQuietRestart()
			if !quiet || minimised != (mode != "shown") {
				fail("the mark read back quiet=%v minimised=%v", quiet, minimised)
			}
			quietShow.Store(quiet)
			quietMinimised.Store(quiet && minimised)

			// every state the window passes through, from its making on
			var hwnd w32.HWND
			var shownWhole bool
			application.InvokeSync(func() { h.openMain("") })
			waitFor("the window is made", func() bool { return h.main != nil })
			application.InvokeSync(func() {
				h.main.OnWindowEvent(events.Windows.WindowShow, func(*application.WindowEvent) {
					if r, _, _ := procIsIconic.Call(uintptr(h.main.NativeWindow())); r == 0 {
						shownWhole = true
					}
				})
			})
			waitFor("the window is up", func() bool {
				hwnd = w32.HWND(uintptr(h.main.NativeWindow()))
				return h.main.IsVisible()
			})
			if mode == "shown" {
				waitFor("the window is open", func() bool { return windowOpen(h.main) })
				if up, min := h.mainUp(); !up || min {
					fail("shown: mainUp says up=%v minimised=%v", up, min)
				}
				drawn("shown")
				fmt.Println("minimised: ok")
				app.Quit()
				return
			}
			// a moment for anything that would show or raise it after
			time.Sleep(time.Second)
			if !onMain(func() bool { return h.main.IsMinimised() }) {
				fail("the window came back not minimised")
			}
			if shownWhole {
				fail("the window was shown whole on its way to the taskbar")
			}
			if w32.GetForegroundWindow() == hwnd {
				fail("the window minimised took the foreground")
			}
			// what the next restart by itself reads of it
			if up, min := h.mainUp(); !up || !min {
				fail("minimised: mainUp says up=%v minimised=%v", up, min)
			}

			// minimised, its page is hidden
			for drained := false; !drained; {
				select {
				case v := <-seen:
					if v == "visible" {
						fail("minimised: the page says it is visible")
					}
				default:
					drained = true
				}
			}

			// restored, as a click on its taskbar button restores it
			procPostMessageW.Call(uintptr(hwnd), w32.WM_SYSCOMMAND, w32.SC_RESTORE, 0)
			waitFor("the window is restored", func() bool { return windowOpen(h.main) })
			if got, want := onMain(h.main.IsMaximised), mode == "minimised-maximised"; got != want {
				fail("restored: maximised=%v, want %v", got, want)
			}
			drawn("restored")
			fmt.Println("minimised: ok")
			app.Quit()
		}()
	})
	if err := app.Run(); err != nil {
		panic(err)
	}
	os.Exit(0)
}
