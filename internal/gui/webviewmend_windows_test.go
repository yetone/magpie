//go:build windows && !nogui

package gui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"golang.org/x/sys/windows"
)

// aquaarboraurum on X: the main window went white, its title bar left, and
// came back only when closed to the tray and opened again. Its WebView2's
// processes are ended here as a crash ends them, and the window comes back
// by itself each time: a renderer's end reloads the page, the GPU
// process's hides and shows the controller (the page sees hidden, then
// visible), and the browser process's makes the window again. Each is
// logged.
//
// The windows are real, so this runs on the Windows box's signed-in
// session, not over ssh (session 0).
const testMendEnv = "MAGPIE_TEST_WEBVIEW_MEND"

func TestWebViewComesBackFromEachProcessEnd(t *testing.T) {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil || session == 0 {
		t.Skip("no interactive desktop (session 0): run it in the signed-in session")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, kill := range []string{"renderer", "gpu-process", "browser"} {
		t.Run(kill, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, self, "-test.run=^$")
			// not t.TempDir: WebView2's processes still hold files in it as
			// the test ends, and its removal fails the test
			home, err := os.MkdirTemp("", "magpie-mend-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(home)
			cmd.Env = append(os.Environ(), testMendEnv+"="+kill, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "LOCALAPPDATA="+filepath.Join(home, "local"), "APPDATA="+filepath.Join(home, "roaming"))
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "mend: ok") {
				t.Fatalf("%s: %v\n%s", kill, err, out)
			}
			for _, l := range strings.Split(string(out), "\n") {
				if strings.Contains(l, "webview (") || strings.Contains(l, "mend: ") {
					t.Log(strings.TrimSpace(l))
				}
			}
		})
	}
}

// webViewBrowserPID is ICoreWebView2's get_BrowserProcessId (slot 37).
func webViewBrowserPID(w *application.WebviewWindow) uint32 {
	wv := chromiumPart(w, "webview")
	if wv == nil {
		return 0
	}
	var pid uint32
	if hr, _, _ := syscall.SyscallN(comMethod(wv, 37), uintptr(wv), uintptr(unsafe.Pointer(&pid))); int32(hr) < 0 {
		return 0
	}
	return pid
}

// webViewChildren are the processes the browser process pid started whose
// command line has --type=<kind>.
func webViewChildren(pid uint32, kind string) ([]uint32, error) {
	ps := fmt.Sprintf(`Get-CimInstance Win32_Process -Filter "ParentProcessId=%d" | Select-Object ProcessId,CommandLine | ConvertTo-Json -Compress`, pid)
	out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
	if err != nil {
		return nil, err
	}
	var procs []struct {
		ProcessId   uint32
		CommandLine string
	}
	s := strings.TrimSpace(string(out))
	if strings.HasPrefix(s, "{") {
		s = "[" + s + "]"
	}
	if err := json.Unmarshal([]byte(s), &procs); err != nil {
		return nil, fmt.Errorf("%v: %s", err, out)
	}
	var pids []uint32
	for _, p := range procs {
		if strings.Contains(p.CommandLine, "--type="+kind+" ") || strings.HasSuffix(p.CommandLine, "--type="+kind) {
			pids = append(pids, p.ProcessId)
		}
	}
	return pids, nil
}

var (
	procGetDC          = user32.NewProc("GetDC")
	procReleaseDC      = user32.NewProc("ReleaseDC")
	procGetClientRect  = user32.NewProc("GetClientRect")
	procClientToScreen = user32.NewProc("ClientToScreen")
	procGetPixel       = windows.NewLazySystemDLL("gdi32.dll").NewProc("GetPixel")
)

// whiteShare is the share of a grid of points over w's client area that
// the screen shows white: the page is blue, so a white one is the window
// gone white (the report). On the main thread.
func whiteShare(w *application.WebviewWindow) float64 {
	hwnd := uintptr(w.NativeWindow())
	var r struct{ L, T, R, B int32 }
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	var o struct{ X, Y int32 }
	procClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&o)))
	dc, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, dc)
	white, all := 0, 0
	for i := 1; i < 10; i++ {
		for j := 1; j < 10; j++ {
			x := o.X + r.L + (r.R-r.L)*int32(i)/10
			y := o.Y + r.T + (r.B-r.T)*int32(j)/10
			c, _, _ := procGetPixel.Call(dc, uintptr(x), uintptr(y))
			if c == 0xFFFFFFFF { // CLR_INVALID: off the screen
				continue
			}
			all++
			if c&0xff > 240 && c>>8&0xff > 240 && c>>16&0xff > 240 {
				white++
			}
		}
	}
	if all == 0 {
		return 1
	}
	return float64(white) / float64(all)
}

func endProcess(pid uint32) error {
	p, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(p)
	return windows.TerminateProcess(p, 1)
}

func init() {
	kill := os.Getenv(testMendEnv)
	if kill == "" {
		return
	}
	fail := func(format string, a ...any) {
		fmt.Printf("mend: "+format+"\n", a...)
		os.Exit(1)
	}
	// the page says when it has loaded, and every 50ms whether it is visible
	loaded := make(chan struct{}, 16)
	seen := make(chan string, 4096)
	app := application.New(application.Options{
		Name:    "magpie webview mend regression",
		Windows: windowsOptions(), // as magpie's: the last window closed leaves the app running
		Assets: application.AssetOptions{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/vis":
				select {
				case seen <- r.URL.Query().Get("s"):
				default:
				}
				return
			case "/loaded":
				loaded <- struct{}{}
				return
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body style="margin:0;background:#2a6f97;height:100vh">magpie<script>fetch("/loaded");const say = () => fetch("/vis?s=" + document.visibilityState);document.addEventListener("visibilitychange", say);setInterval(say, 50)</script></body></html>`)
		})},
	})
	h := &host{app: app}
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
	waitLoaded := func(what string) {
		select {
		case <-loaded:
		case <-time.After(30 * time.Second):
			fail("%s: the page didn't load", what)
		}
	}
	drain := func() {
		for {
			select {
			case <-seen:
			default:
				return
			}
		}
	}
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			application.InvokeSync(func() { h.openMain("") })
			waitFor("the window is made", func() bool { return h.main != nil })
			waitLoaded("first")
			waitFor("the window is open", func() bool { return windowOpen(h.main) })
			// the handler is set as the first page comes, on the main thread
			time.Sleep(time.Second)
			first := h.main
			application.InvokeSync(func() { first.SetAlwaysOnTop(true); first.Focus() })
			waitFor("the page is drawn", func() bool { return whiteShare(first) < 0.5 })
			var pid uint32
			onMain(func() bool { pid = webViewBrowserPID(first); return true })
			if pid == 0 {
				fail("no browser process id")
			}
			drain()
			switch kill {
			case "renderer", "gpu-process":
				pids, err := webViewChildren(pid, kill)
				if err != nil || len(pids) == 0 {
					fail("no %s under the browser process %d: %v", kill, pid, err)
				}
				for _, p := range pids {
					if err := endProcess(p); err != nil {
						fail("ending %s %d: %v", kill, p, err)
					}
				}
			case "browser":
				if err := endProcess(pid); err != nil {
					fail("ending the browser process %d: %v", pid, err)
				}
			}
			switch kill {
			case "renderer":
				waitLoaded("after the renderer ended")
			case "gpu-process":
				// hidden, then visible again: the controller was hidden and shown
				deadline := time.After(30 * time.Second)
				hidden := false
				for done := false; !done; {
					select {
					case v := <-seen:
						if v == "hidden" {
							hidden = true
						} else if hidden && v == "visible" {
							done = true
						}
					case <-deadline:
						fail("after the GPU process ended: hidden=%v, never visible again after", hidden)
					}
				}
			case "browser":
				waitFor("the window is made again", func() bool { return h.main != nil && h.main != first })
				waitLoaded("after the browser process ended")
				waitFor("the new window is open", func() bool { return windowOpen(h.main) })
			}
			// drawn, not white
			time.Sleep(time.Second)
			var share float64
			waitFor("the page is drawn again", func() bool {
				application.InvokeSync(func() { h.main.SetAlwaysOnTop(true) })
				share = whiteShare(h.main)
				return share < 0.5
			})
			fmt.Printf("mend: white share after %s ended: %.2f\n", kill, share)
			fmt.Println("mend: ok")
			app.Quit()
		}()
	})
	if err := app.Run(); err != nil {
		panic(err)
	}
	os.Exit(0)
}
