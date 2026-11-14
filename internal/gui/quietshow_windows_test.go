//go:build windows && !nogui

package gui

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/w32"
	"golang.org/x/sys/windows"
)

// shangyuan0818 on X: with Settings' AutoRestart, the window a restart by
// itself brings back flashed on the taskbar, asking for attention, while
// the user was in another app. The new magpie, started by the old one in
// the background, has no right to the foreground; Wails' Show (SW_SHOW)
// and Focus (SetForegroundWindow) ask for it all the same, and Windows
// answers a refused ask by flashing the window's taskbar button until it
// is clicked. Where the ask is allowed (nobody at the keyboard for a
// while), the window took the foreground from the app in front instead.
// Brought back quietly, it is shown behind, neither activated nor asking
// for attention, its page drawn. One closed to the tray stays hidden.
//
// The windows are real, so this runs where Windows has a desktop the user
// sees: on the Windows box's signed-in session, not over ssh (session 0).
const testQuietShowEnv = "MAGPIE_TEST_QUIET_SHOW"

func TestRestartBySelfAsksNoAttention(t *testing.T) {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil || session == 0 {
		t.Skip("no interactive desktop (session 0): run it in the signed-in session")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"window", "tray"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, self, "-test.run=^$")
			cmd.Env = append(os.Environ(), testQuietShowEnv+"="+mode, "XDG_CONFIG_HOME="+t.TempDir(), "LOCALAPPDATA="+t.TempDir())
			out, err := cmd.CombinedOutput()
			t.Logf("%s", out)
			if err != nil || !strings.Contains(string(out), "quiet: ok") {
				t.Fatalf("%s: %v", mode, err)
			}
		})
	}
}

var (
	procRegisterShellHookWindow = user32.NewProc("RegisterShellHookWindow")
	procRegisterWindowMessageW  = user32.NewProc("RegisterWindowMessageW")
	procCreateWindowExW         = user32.NewProc("CreateWindowExW")
	procDestroyWindow           = user32.NewProc("DestroyWindow")
)

// The shell hook's codes (WinUser.h).
const (
	hshellWindowActivated  = 4
	hshellRudeAppActivated = 0x8004
	hshellFlash            = 0x8006
)

// otherForeground puts a plain window of this process in front, as the
// app the user is in, and says whether it got the foreground.
func otherForeground() (uintptr, bool) {
	cls, _ := windows.UTF16PtrFromString("STATIC")
	title, _ := windows.UTF16PtrFromString("the app the user is in")
	hwnd, _, _ := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		w32.WS_OVERLAPPEDWINDOW|w32.WS_VISIBLE, 40, 40, 400, 300, 0, 0, 0, 0)
	if hwnd == 0 {
		return 0, false
	}
	w32.SetForegroundWindow(w32.HWND(hwnd))
	return hwnd, w32.GetForegroundWindow() == w32.HWND(hwnd)
}

func init() {
	mode := os.Getenv(testQuietShowEnv)
	if mode == "" {
		return
	}
	fail := func(format string, a ...any) {
		fmt.Printf("quiet: FAIL "+format+"\n", a...)
		os.Exit(1)
	}
	pid := windows.GetCurrentProcessId()
	// what the shell was told of this process's windows
	var mu sync.Mutex
	type shellNote struct {
		at   time.Time
		code uintptr
		hwnd uintptr
	}
	var told []shellNote
	name, _ := windows.UTF16PtrFromString("SHELLHOOK")
	shellMsg, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(name)))
	inner := appWndProc
	appWndProc = func(hwnd uintptr, msg uint32, wParam, lParam uintptr) (uintptr, bool) {
		if shellMsg != 0 && uintptr(msg) == shellMsg {
			var p uint32
			windows.GetWindowThreadProcessId(windows.HWND(lParam), &p)
			switch wParam {
			case hshellFlash, hshellWindowActivated, hshellRudeAppActivated:
				if p == pid && lParam != 0 {
					mu.Lock()
					told = append(told, shellNote{time.Now(), wParam, lParam})
					mu.Unlock()
				}
			}
		}
		return inner(hwnd, msg, wParam, lParam)
	}
	seen := make(chan string, 1024)
	app := application.New(application.Options{
		Name:    "magpie quiet show regression",
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
			fmt.Fprint(w, `<html><body style="margin:0;background:#2a6f97;height:100vh">magpie<script>setInterval(() => fetch("/vis?s=" + document.visibilityState), 50)</script></body></html>`)
		})},
	})
	h := &host{app: app, ready: make(chan struct{})}
	onMain := func(f func() bool) (b bool) {
		application.InvokeSync(func() { b = f() })
		return
	}
	waitFor := func(what string, ok func() bool) {
		for deadline := time.Now().Add(30 * time.Second); !onMain(ok); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				fail("timed out waiting until %s", what)
			}
		}
	}
	// the app the user is in, in front before the new magpie makes its
	// windows, as the old one's restart leaves it
	var other uintptr
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			application.InvokeSync(func() {
				if r, _, err := procRegisterShellHookWindow.Call(ownMainThreadWindow()); r == 0 {
					fail("no shell hook: %v", err)
				}
				var front bool
				other, front = otherForeground()
				fmt.Printf("quiet: the other window has the foreground: %v\n", front)
			})
			// what the old magpie left as it restarted by itself, read as a
			// start reads it (app.go)
			markQuietRestart(false)
			quiet, _ := takeQuietRestart()
			showMain := mode == "window"
			quietShow.Store(quiet && showMain)
			quietMinimised.Store(false)
			var navigated sync.Once
			loaded := make(chan struct{})
			application.InvokeSync(func() {
				h.panel = h.makePanel()
				h.main = h.makeMain("/")
				h.main.OnWindowEvent(events.Windows.WebViewNavigationCompleted, func(*application.WindowEvent) {
					navigated.Do(func() { close(loaded) })
				})
			})
			select {
			case <-loaded:
			case <-time.After(30 * time.Second):
				fail("the page didn't load")
			}
			if showMain {
				h.ShowMain("")
				waitFor("the window is up", func() bool { return h.main.IsVisible() })
			}
			// a moment for anything that would raise it or ask after
			time.Sleep(3 * time.Second)
			hwnd := w32.HWND(uintptr(h.main.NativeWindow()))
			fmt.Printf("quiet: other %#x, main %#x, panel %#x\n", other, uintptr(hwnd), uintptr(h.panel.NativeWindow()))
			// what the shell heard of magpie's windows, the other one left out
			var asked []string
			mu.Lock()
			for _, n := range told {
				if n.hwnd != other {
					asked = append(asked, fmt.Sprintf("%s code %#x for %#x", n.at.Format("15:04:05.000"), n.code, n.hwnd))
				}
			}
			mu.Unlock()
			if len(asked) > 0 {
				fail("%s: the shell was asked for attention: %v", mode, asked)
			}
			if fg := w32.GetForegroundWindow(); fg == hwnd || fg == w32.HWND(uintptr(h.panel.NativeWindow())) {
				fail("%s: magpie's window took the foreground", mode)
			}
			if showMain {
				if up, min := h.mainUp(); !up || min {
					fail("mainUp says up=%v minimised=%v", up, min)
				}
				// drawn: its page says it is visible, not a blank webview
				for drawn := false; !drawn; {
					select {
					case v := <-seen:
						drawn = v == "visible"
					case <-time.After(10 * time.Second):
						fail("the page isn't visible")
					}
				}
			} else if onMain(h.main.IsVisible) {
				fail("tray: the window came up")
			}
			procDestroyWindow.Call(other)
			fmt.Println("quiet: ok")
			app.Quit()
		}()
	})
	if err := app.Run(); err != nil {
		panic(err)
	}
	os.Exit(0)
}
