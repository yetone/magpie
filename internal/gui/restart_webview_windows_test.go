//go:build windows && !nogui

package gui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"golang.org/x/sys/windows"

	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/update"
)

// vsoullai on X (0.1.4096, Windows): the restart after an update didn't
// wait for the old magpie's WebView2 to let go of its profile folder, and
// the new magpie hung. The restart is played here as magpie makes it on
// Windows, with Wails' single instance on as the app has it: an old magpie
// with its window up starts the new one detached and quits; the new one
// starts as Run does (AwaitPredecessor, webView2Prepare's wait,
// application.New) and must come up with its own window and page.
// slow: the old one takes 25s to quit, past the 20s the start once waited.
//
// The windows are real, so this runs on the Windows box's signed-in
// session, not over ssh (session 0).
const testRestartWebViewEnv = "MAGPIE_TEST_RESTART_WEBVIEW"

func TestRestartWaitsForTheOldWebView(t *testing.T) {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil || session == 0 {
		t.Skip("no interactive desktop (session 0): run it in the signed-in session")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"quick", "slow"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
			defer cancel()
			// not t.TempDir: WebView2's processes still hold files in it
			home, err := os.MkdirTemp("", "magpie-restart-webview-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(home)
			result := filepath.Join(home, "result.txt")
			id := fmt.Sprintf("test.restart-webview.%d.%d", os.Getpid(), time.Now().UnixNano())
			cmd := exec.CommandContext(ctx, self, "-test.run=^$")
			cmd.Env = append(os.Environ(), testRestartWebViewEnv+"=old,"+mode+","+id+","+result,
				"XDG_CONFIG_HOME="+filepath.Join(home, "config"), "LOCALAPPDATA="+filepath.Join(home, "local"), "APPDATA="+filepath.Join(home, "roaming"))
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "restart webview: old done") {
				t.Fatalf("old: %v\n%s", err, out)
			}
			var got []byte
			for deadline := time.Now().Add(120 * time.Second); ; time.Sleep(250 * time.Millisecond) {
				if got, err = os.ReadFile(result); err == nil && strings.Contains(string(got), "restart webview: end") {
					break
				}
				if time.Now().After(deadline) {
					part, _ := os.ReadFile(result + ".part")
					nout, _ := os.ReadFile(result + ".out")
					t.Fatalf("%s: the new one never came up with its window: %s\n-- its output:\n%s", mode, part, nout)
				}
			}
			for _, l := range strings.Split(strings.TrimSpace(string(got)), "\n") {
				t.Log(strings.TrimSpace(l))
			}
			if !strings.Contains(string(got), "restart webview: ok") {
				t.Fatalf("%s: the new one didn't come up with its window", mode)
			}
		})
	}
}

func init() {
	v := os.Getenv(testRestartWebViewEnv)
	if v == "" {
		return
	}
	parts := strings.SplitN(v, ",", 4)
	if len(parts) != 4 {
		panic("restart webview: " + v)
	}
	role, mode, id, result := parts[0], parts[1], parts[2], parts[3]
	if role == "old" {
		restartWebViewOld(mode, id, func(format string, a ...any) { fmt.Printf("restart webview: "+format+"\n", a...) })
		os.Exit(0)
	}
	f, err := os.OpenFile(result+".part", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		panic(err)
	}
	start := time.Now()
	report := func(format string, a ...any) {
		fmt.Fprintf(f, "restart webview: "+format+" (at %.2fs)\n", append(a, time.Since(start).Seconds())...)
		f.Sync()
	}
	end := func(ok bool) {
		if ok {
			report("ok")
		}
		report("end")
		f.Close()
		os.Rename(result+".part", result)
	}
	report("new started, pid %d", os.Getpid())
	restartWebViewNew(id, report, end)
	os.Exit(0)
}

// restartWebViewOld is the magpie before: Wails' single instance on, its
// window up and drawn; it starts the new one as update's launch does and
// quits, taking 25s over it when slow.
func restartWebViewOld(mode, id string, report func(string, ...any)) {
	fail := func(format string, a ...any) { report(format, a...); os.Exit(1) }
	loaded := make(chan struct{}, 16)
	seen := make(chan string, 4096)
	app := application.New(application.Options{
		Name:           "magpie restart webview regression",
		SingleInstance: &application.SingleInstanceOptions{UniqueID: id},
		Windows:        windowsOptions(),
		Assets:         application.AssetOptions{Handler: restartDrawPage(loaded, seen)},
		OnShutdown: func() {
			if mode == "slow" {
				time.Sleep(25 * time.Second)
			}
		},
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
			self, _ := os.Executable()
			cmd := proc.Command(self, "-test.run=^$")
			cmd.Env = append(os.Environ(), "MAGPIE_REPLACES="+strconv.Itoa(os.Getpid()),
				testRestartWebViewEnv+"=new,"+strings.SplitN(os.Getenv(testRestartWebViewEnv), ",", 2)[1])
			if cmd.SysProcAttr == nil {
				cmd.SysProcAttr = &syscall.SysProcAttr{}
			}
			cmd.SysProcAttr.CreationFlags |= 0x00000008 | 0x00000200 // DETACHED_PROCESS, CREATE_NEW_PROCESS_GROUP
			out, _ := os.Create(strings.SplitN(os.Getenv(testRestartWebViewEnv), ",", 4)[3] + ".out")
			cmd.Stdout, cmd.Stderr = out, out
			if err := cmd.Start(); err != nil {
				fail("old: starting the new one: %v", err)
			}
			cmd.Process.Release()
			report("old done")
			_ = out
			app.Quit()
		}()
	})
	if err := app.Run(); err != nil {
		panic(err)
	}
}

// restartWebViewNew is the version restarted into: it starts as Run does
// and says whether its window's page came.
func restartWebViewNew(id string, report func(string, ...any), end func(bool)) {
	dir := webView2Dir()
	// what happens to the old one, for the record
	if pid, err := strconv.Atoi(os.Getenv("MAGPIE_REPLACES")); err == nil {
		go func() {
			if h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid)); err == nil {
				windows.WaitForSingleObject(h, windows.INFINITE)
				windows.CloseHandle(h)
			}
			report("the old magpie %d ended", pid)
			for !webView2Free(dir) {
				time.Sleep(50 * time.Millisecond)
			}
			report("profile folder's lockfile free")
		}()
	}
	report("waiting as Run does")
	update.AwaitPredecessor()
	webView2Ready(dir, id, 30*time.Second)
	report("making the app")
	loaded := make(chan struct{}, 16)
	seen := make(chan string, 4096)
	var app *application.App
	app = application.New(application.Options{
		Name:           "magpie restart webview regression",
		SingleInstance: &application.SingleInstanceOptions{UniqueID: id},
		Windows:        windowsOptions(),
		Assets:         application.AssetOptions{Handler: restartDrawPage(loaded, seen)},
		ErrorHandler: func(err error) {
			report("app error: %v", err)
			end(false)
		},
	})
	report("the app is made: this one is the instance")
	h := &host{app: app}
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			ok := false
			defer func() { end(ok); app.Quit() }()
			application.InvokeSync(func() { h.openMain("") })
			select {
			case <-loaded:
				report("the page loaded")
				ok = true
			case <-time.After(45 * time.Second):
				report("the page didn't load")
			}
		}()
	})
	if err := app.Run(); err != nil {
		report("run: %v", err)
	}
}
