//go:build darwin && cgo && !nogui

package gui

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// The main window open under another window is open (#1381). Lightweight
// mode took the occlusion state for shown, so the window the user had
// switched away from with Command-Tab was let go as closed, and switching
// back found it blank or gone. Hidden with magpie (Command-H) or minimised,
// it is open too; closed by magpie, it isn't.
func TestCoveredWindowStaysUp(t *testing.T) {
	out, err := runAppKit(t, 30*time.Second, []string{"MAGPIE_TEST_WINDOW_UP=1"})
	if err != nil || !strings.Contains(string(out), "window up: ok") {
		t.Fatalf("window up: %v\n%s", err, out)
	}
}

func init() {
	if os.Getenv("MAGPIE_TEST_WINDOW_UP") != "1" {
		return
	}
	app := application.New(application.Options{
		Name: "magpie window up regression",
		Mac:  application.MacOptions{ActivationPolicy: application.ActivationPolicyAccessory},
		Assets: application.AssetOptions{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<p>magpie</p>`)
		})},
	})
	h := &host{app: app}
	fail := func(format string, a ...any) {
		fmt.Printf("window up: "+format+"\n", a...)
		os.Exit(1)
	}
	onMain := func(f func() bool) (b bool) {
		application.InvokeSync(func() { b = f() })
		return
	}
	waitFor := func(what string, ok func() bool) {
		for deadline := time.Now().Add(5 * time.Second); !onMain(ok); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				fail("timed out waiting until %s", what)
			}
		}
	}
	// kept: what the Dock, a restart and lightweight mode go by
	kept := func() bool { return h.mainShown() && h.lightShown(h.main) }
	expect := func(how string, want bool) {
		if got := onMain(kept); got != want {
			fail("%s: the window counts as up=%v, want %v", how, got, want)
		}
	}
	watchHide()
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			application.InvokeSync(func() { h.openMain("") })
			waitFor("the window is open", func() bool { return windowOpen(h.main) })
			expect("open", true)

			// covered by another window, as by the app Command-Tab went to
			var cover *application.WebviewWindow
			application.InvokeSync(func() {
				x, y := h.main.Position()
				wd, ht := h.main.Size()
				cover = app.Window.NewWithOptions(application.WebviewWindowOptions{
					Name: "cover", Width: wd + 200, Height: ht + 200,
					Frameless: true, AlwaysOnTop: true,
					BackgroundColour: application.NewRGB(0, 128, 128),
				})
				cover.SetPosition(x-100, y-100)
				cover.Show()
			})
			waitFor("the cover hides the window from view", func() bool { return !h.main.IsVisible() })
			expect("covered", true)
			application.InvokeSync(func() { cover.Close() })

			// hidden with magpie, as by Command-H
			application.InvokeSync(app.Hide)
			waitFor("magpie is hidden", func() bool { return !windowOpen(h.main) })
			expect("hidden with magpie", true)
			application.InvokeSync(app.Show)
			waitFor("magpie is shown again", func() bool { return windowOpen(h.main) })

			// minimised
			application.InvokeSync(func() { h.main.Minimise() })
			waitFor("the window is minimised", func() bool { return h.main.IsMinimised() })
			expect("minimised", true)
			application.InvokeSync(func() { h.main.UnMinimise() })
			waitFor("the window is back", func() bool { return windowOpen(h.main) })

			// closed by magpie
			h.hideMain()
			waitFor("the window is closed", func() bool { return !windowOpen(h.main) })
			expect("closed", false)

			// closed, then magpie hidden: still closed
			application.InvokeSync(app.Hide)
			time.Sleep(300 * time.Millisecond)
			expect("closed, magpie hidden", false)
			application.InvokeSync(app.Show)

			fmt.Println("window up: ok")
			app.Quit()
		}()
	})
	if err := app.Run(); err != nil {
		panic(err)
	}
	os.Exit(0)
}
