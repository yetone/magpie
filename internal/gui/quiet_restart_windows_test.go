//go:build windows && !nogui

package gui

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"golang.org/x/sys/windows"

	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// aquaarboraurum on X: the window a restart to update brings back is
// white, every time, its title bar there and its page alive; a restart by
// hand, or a reload, draws it. The restart is played here as magpie makes
// it on Windows: an old process with its window up starts the new one
// detached (update's launch), marks the restart as one by itself
// (markQuietRestart) and quits; the new one waits for it to go
// (AwaitPredecessor), for its WebView2 profile (webView2Ready), reads the
// mark and opens its window as the app does (openMain). The new window's
// page must be drawn: its own pixels (PrintWindow) not white, and the
// page told it is visible.
//
// The windows are real, so this runs on the Windows box's signed-in
// session, not over ssh (session 0).
const testRestartDrawEnv = "MAGPIE_TEST_RESTART_DRAW"

func TestRestartedWindowIsDrawn(t *testing.T) {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil || session == 0 {
		t.Skip("no interactive desktop (session 0): run it in the signed-in session")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"quiet", "asked", "quiet-behind", "asked-behind", "quiet-behind-max", "asked-behind-max", "quiet-cover", "asked-cover", "quiet-cover-max", "quiet-cloak", "asked-cloak", "quiet-min", "quiet-min-max"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			// not t.TempDir: WebView2's processes still hold files in it
			home, err := os.MkdirTemp("", "magpie-restart-draw-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(home)
			result := filepath.Join(home, "result.txt")
			cmd := exec.CommandContext(ctx, self, "-test.run=^$")
			cmd.Env = append(os.Environ(), testRestartDrawEnv+"=old,"+mode+","+result,
				"XDG_CONFIG_HOME="+filepath.Join(home, "config"), "LOCALAPPDATA="+filepath.Join(home, "local"), "APPDATA="+filepath.Join(home, "roaming"))
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "restart draw: old done") {
				t.Fatalf("old: %v\n%s", err, out)
			}
			var got []byte
			for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(250 * time.Millisecond) {
				if got, err = os.ReadFile(result); err == nil && strings.Contains(string(got), "restart draw: end") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the new one wrote nothing in time: %s", got)
				}
			}
			for _, l := range strings.Split(strings.TrimSpace(string(got)), "\n") {
				t.Log(strings.TrimSpace(l))
			}
			if !strings.Contains(string(got), "restart draw: ok") {
				t.Fatalf("%s: the window the restart brought back wasn't drawn", mode)
			}
		})
	}
}

var (
	procPrintWindow            = user32.NewProc("PrintWindow")
	procWindowFromPoint        = user32.NewProc("WindowFromPoint")
	procGetAncestor            = user32.NewProc("GetAncestor")
	procCreateCompatibleDC     = windows.NewLazySystemDLL("gdi32.dll").NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = windows.NewLazySystemDLL("gdi32.dll").NewProc("CreateCompatibleBitmap")
	procSelectObject           = windows.NewLazySystemDLL("gdi32.dll").NewProc("SelectObject")
	procDeleteObject           = windows.NewLazySystemDLL("gdi32.dll").NewProc("DeleteObject")
	procDeleteDC               = windows.NewLazySystemDLL("gdi32.dll").NewProc("DeleteDC")
	procDwmSetWindowAttribute  = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
)

// ownWhiteShare is whiteShare over the window's own pixels, as DWM
// composes them (PrintWindow, PW_RENDERFULLCONTENT): what the window
// shows whether another covers it or not. On the main thread.
func ownWhiteShare(w *application.WebviewWindow) float64 {
	hwnd := uintptr(w.NativeWindow())
	var r struct{ L, T, R, B int32 }
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if r.R-r.L <= 0 || r.B-r.T <= 0 {
		return 1
	}
	screen, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, screen)
	dc, _, _ := procCreateCompatibleDC.Call(screen)
	defer procDeleteDC.Call(dc)
	bmp, _, _ := procCreateCompatibleBitmap.Call(screen, uintptr(r.R-r.L), uintptr(r.B-r.T))
	defer procDeleteObject.Call(bmp)
	old, _, _ := procSelectObject.Call(dc, bmp)
	defer procSelectObject.Call(dc, old)
	const pwClientOnly, pwRenderFullContent = 1, 2
	if ok, _, _ := procPrintWindow.Call(hwnd, dc, pwClientOnly|pwRenderFullContent); ok == 0 {
		return 1
	}
	white, all := 0, 0
	for i := 1; i < 10; i++ {
		for j := 1; j < 10; j++ {
			c, _, _ := procGetPixel.Call(dc, uintptr((r.R-r.L)*int32(i)/10), uintptr((r.B-r.T)*int32(j)/10))
			if c == 0xFFFFFFFF {
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

// screenWhiteShare is whiteShare over the points of w's client area where
// the screen shows w (not another window over it), and how many there were.
func screenWhiteShare(w *application.WebviewWindow) (share float64, seen int) {
	hwnd := uintptr(w.NativeWindow())
	var r struct{ L, T, R, B int32 }
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	var o struct{ X, Y int32 }
	procClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&o)))
	dc, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, dc)
	white := 0
	for i := 1; i < 10; i++ {
		for j := 1; j < 10; j++ {
			x := o.X + r.L + (r.R-r.L)*int32(i)/10
			y := o.Y + r.T + (r.B-r.T)*int32(j)/10
			at, _, _ := procWindowFromPoint.Call(uintptr(uint32(x)) | uintptr(uint32(y))<<32)
			const gaRoot = 2
			if root, _, _ := procGetAncestor.Call(at, gaRoot); root != hwnd {
				continue
			}
			c, _, _ := procGetPixel.Call(dc, uintptr(x), uintptr(y))
			if c == 0xFFFFFFFF {
				continue
			}
			seen++
			if c&0xff > 240 && c>>8&0xff > 240 && c>>16&0xff > 240 {
				white++
			}
		}
	}
	if seen == 0 {
		return 0, 0
	}
	return float64(white) / float64(seen), seen
}

func init() {
	v := os.Getenv(testRestartDrawEnv)
	if v == "" {
		return
	}
	parts := strings.SplitN(v, ",", 3)
	if len(parts) != 3 {
		panic("restart draw: " + v)
	}
	role, mode, result := parts[0], parts[1], parts[2]
	var report func(format string, a ...any)
	if role == "old" {
		report = func(format string, a ...any) { fmt.Printf("restart draw: "+format+"\n", a...) }
	} else {
		f, err := os.OpenFile(result+".part", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			panic(err)
		}
		report = func(format string, a ...any) { fmt.Fprintf(f, "restart draw: "+format+"\n", a...); f.Sync() }
		end := func(ok bool) {
			if ok {
				report("ok")
			}
			report("end")
			f.Close()
			os.Rename(result+".part", result)
		}
		defer func() {
			if r := recover(); r != nil {
				report("panic: %v", r)
				end(false)
				os.Exit(1)
			}
		}()
		restartDrawNew(mode, report, end)
		os.Exit(0)
	}
	restartDrawOld(mode, report)
	os.Exit(0)
}

// restartDrawPage is blue, says once it has loaded, and every 50ms
// whether it is visible.
func restartDrawPage(loaded chan<- struct{}, seen chan<- string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vis":
			select {
			case seen <- r.URL.Query().Get("s"):
			default:
			}
			return
		case "/loaded":
			select {
			case loaded <- struct{}{}:
			default:
			}
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body style="margin:0;background:#2a6f97;height:100vh">magpie<script>fetch("/loaded");const say = () => fetch("/vis?s=" + document.visibilityState);document.addEventListener("visibilitychange", say);setInterval(say, 50)</script></body></html>`)
	})
}

// restartDrawOld is the magpie before: its window up and drawn, it starts
// the new one as update's launch does and quits.
func restartDrawOld(mode string, report func(string, ...any)) {
	fail := func(format string, a ...any) { report(format, a...); os.Exit(1) }
	loaded := make(chan struct{}, 16)
	seen := make(chan string, 4096)
	app := application.New(application.Options{
		Name:    "magpie restart draw regression",
		Windows: windowsOptions(),
		Assets:  application.AssetOptions{Handler: restartDrawPage(loaded, seen)},
	})
	h := &host{app: app}
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			application.InvokeSync(func() { h.openMain("") })
			select {
			case <-loaded:
			case <-time.After(30 * time.Second):
				fail("old: the page didn't load")
			}
			time.Sleep(time.Second)
			if strings.Contains(mode, "-max") {
				s := settings.Load()
				s.WindowMaximised = true
				settings.Save(s)
			}
			if strings.Contains(mode, "behind") || strings.Contains(mode, "cover") {
				// another app in front, as the user is in one when a
				// restart by itself comes: a small window at the corner,
				// or (cover) one over the whole screen, the window shown
				// under it
				form := `$f.StartPosition = 'Manual'; $f.Left = 0; $f.Top = 0; $f.Width = 240; $f.Height = 160`
				if strings.Contains(mode, "cover") {
					form = `$f.FormBorderStyle = 'None'; $f.TopMost = $true; $f.WindowState = 'Maximized'; $f.BackColor = 'Black'`
				}
				ps := exec.Command("powershell", "-NoProfile", "-Command",
					`Add-Type -AssemblyName System.Windows.Forms; $f = New-Object System.Windows.Forms.Form; `+form+`; $f.Text = 'other app'; $f.ShowDialog()`)
				if err := ps.Start(); err != nil {
					fail("old: the other app: %v", err)
				}
				other := uint32(ps.Process.Pid)
				os.Setenv("MAGPIE_TEST_OTHER_PID", strconv.Itoa(int(other)))
				for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(100 * time.Millisecond) {
					var pid uint32
					windows.GetWindowThreadProcessId(windows.GetForegroundWindow(), &pid)
					if pid == other {
						break
					}
					if time.Now().After(deadline) {
						fail("old: the other app never came to the front")
					}
				}
			}
			if strings.Contains(mode, "-min") {
				// minimised on the taskbar as the restart comes (#3495)
				application.InvokeSync(func() { h.main.Minimise() })
				time.Sleep(time.Second)
			}
			if strings.HasPrefix(mode, "quiet") {
				markQuietRestart(strings.Contains(mode, "-min"))
			} else {
				unmarkQuietRestart()
			}
			self, _ := os.Executable()
			// as update's launch starts the new version: detached, no
			// console, its window on the desktop this one is on
			cmd := proc.Command(self, "-test.run=^$")
			cmd.Env = append(os.Environ(), "MAGPIE_REPLACES="+strconv.Itoa(os.Getpid()),
				testRestartDrawEnv+"=new,"+mode+","+strings.SplitN(os.Getenv(testRestartDrawEnv), ",", 3)[2])
			if cmd.SysProcAttr == nil {
				cmd.SysProcAttr = &syscall.SysProcAttr{}
			}
			cmd.SysProcAttr.CreationFlags |= 0x00000008 | 0x00000200 // DETACHED_PROCESS, CREATE_NEW_PROCESS_GROUP
			if err := cmd.Start(); err != nil {
				fail("old: starting the new one: %v", err)
			}
			cmd.Process.Release()
			time.Sleep(time.Second)
			report("old done")
			app.Quit()
		}()
	})
	if err := app.Run(); err != nil {
		panic(err)
	}
}

// restartDrawNew is the version restarted into: it starts as the app does
// and says what its window shows.
func restartDrawNew(mode string, report func(string, ...any), end func(bool)) {
	update.AwaitPredecessor()
	webView2Ready(webView2Dir(), "restart-draw", 30*time.Second)
	quiet, minimised := takeQuietRestart()
	if quiet != strings.HasPrefix(mode, "quiet") || minimised != strings.Contains(mode, "-min") {
		report("the mark read back quiet=%v minimised=%v", quiet, minimised)
		end(false)
		os.Exit(1)
	}
	quietShow.Store(quiet)
	quietMinimised.Store(quiet && minimised)
	loaded := make(chan struct{}, 16)
	seen := make(chan string, 4096)
	app := application.New(application.Options{
		Name:    "magpie restart draw regression",
		Windows: windowsOptions(),
		Assets:  application.AssetOptions{Handler: restartDrawPage(loaded, seen)},
	})
	h := &host{app: app}
	ok := true
	logged := &lockedLog{}
	log.SetOutput(logged)
	h.mendOnWake()
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			defer func() {
				if pid, err := strconv.Atoi(os.Getenv("MAGPIE_TEST_OTHER_PID")); err == nil {
					endProcess(uint32(pid))
				}
				end(ok)
				app.Quit()
			}()
			cloak := func(on bool) {
				v := uint32(0)
				if on {
					v = 1
				}
				r, _, _ := procDwmSetWindowAttribute.Call(uintptr(h.main.NativeWindow()), 13, uintptr(unsafe.Pointer(&v)), 4) // DWMWA_CLOAK
				report("cloak %v: %#x", on, r)
			}
			application.InvokeSync(func() {
				h.openMain("")
				// shown while nothing of it can be seen, as on a locked
				// screen: the window cloaked from before it is shown
				if strings.Contains(mode, "cloak") {
					cloak(true)
				}
			})
			select {
			case <-loaded:
			case <-time.After(30 * time.Second):
				report("the page didn't load")
				ok = false
				return
			}
			if strings.Contains(mode, "-min") {
				// on the taskbar, then restored as a click on its button does
				var up bool
				for deadline := time.Now().Add(15 * time.Second); !up && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
					application.InvokeSync(func() { up = h.main != nil && h.main.IsVisible() })
				}
				time.Sleep(2 * time.Second)
				procPostMessageW.Call(uintptr(h.main.NativeWindow()), 0x0112, 0xF120, 0) // WM_SYSCOMMAND, SC_RESTORE
			}
			var open bool
			for deadline := time.Now().Add(15 * time.Second); !open && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
				application.InvokeSync(func() { open = h.main != nil && windowOpen(h.main) })
			}
			if !open {
				report("the window never opened")
				ok = false
				return
			}
			if strings.Contains(mode, "cloak") {
				time.Sleep(3 * time.Second)
				application.InvokeSync(func() { cloak(false) })
				time.Sleep(500 * time.Millisecond)
			}
			if strings.Contains(mode, "cover") {
				// the cover goes after the window has been up under it
				time.Sleep(2 * time.Second)
				if pid, err := strconv.Atoi(os.Getenv("MAGPIE_TEST_OTHER_PID")); err == nil {
					endProcess(uint32(pid))
				}
				time.Sleep(500 * time.Millisecond)
			}
			// what the window shows over the next seconds, nothing touching it
			last := ""
			var own, scr float64
			var seenPts int
			var maxed bool
			for i := 0; i < 16; i++ {
				time.Sleep(500 * time.Millisecond)
				for drained := false; !drained; {
					select {
					case s := <-seen:
						last = s
					default:
						drained = true
					}
				}
				application.InvokeSync(func() {
					own = ownWhiteShare(h.main)
					scr, seenPts = screenWhiteShare(h.main)
					maxed = h.main.IsMaximised()
				})
				fg := windows.GetForegroundWindow() == windows.HWND(uintptr(h.main.NativeWindow()))
				report("%4.1fs page %s, own white %.2f, screen white %.2f over %d points, foreground %v, maximised %v", float64(i+1)/2, last, own, scr, seenPts, fg, maxed)
			}
			if last != "visible" || own > 0.5 || seenPts > 0 && scr > 0.5 {
				ok = false
			}
			// the screen back: an unlock reaches the app's own main-thread
			// window and draws the open window again, which stays drawn
			if w := ownMainThreadWindow(); w == 0 {
				report("no main-thread window of our own")
				ok = false
			} else {
				procPostMessageW.Call(w, wmWTSSessionChange, wtsSessionUnlock, 0)
			}
			time.Sleep(1500 * time.Millisecond)
			application.InvokeSync(func() { own = ownWhiteShare(h.main) })
			got := logged.String()
			report("after an unlock: own white %.2f; log:\n%s", own, got)
			if !strings.Contains(got, "the session was unlocked; drawing it again") || strings.Contains(got, "doesn't draw the windows again") || own > 0.5 {
				ok = false
			}
			// the WebView2 browser started without occlusion tracking
			cmd, _ := proc.Command("powershell", "-NoProfile", "-Command",
				fmt.Sprintf("(Get-CimInstance Win32_Process -Filter \"Name='msedgewebview2.exe' AND ParentProcessId=%d\").CommandLine", os.Getpid())).Output()
			report("browser: %s", strings.TrimSpace(string(cmd)))
			if !strings.Contains(string(cmd), "CalculateNativeWinOcclusion") {
				ok = false
			}
		}()
	})
	if err := app.Run(); err != nil {
		panic(err)
	}
}

// lockedLog keeps what the app logs, for the checks to read.
type lockedLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
