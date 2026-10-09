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

// The system's close on the quick panel (KWin's title bar X on KDE, #1283;
// Alt+F4; a window manager's close key) reaches magpie as WindowClosing, as
// Close sends it. The panel is hidden and stays the tray's, so the icon
// opens it again; closed, the icon did nothing until a restart. A panel
// lightweight mode let go still closes.
func TestPanelCloseHidesIt(t *testing.T) {
	out, err := runAppKit(t, 30*time.Second, []string{"MAGPIE_TEST_PANEL_CLOSE=1"})
	if err != nil || !strings.Contains(string(out), "panel close: ok") {
		t.Fatalf("panel close: %v\n%s", err, out)
	}
}

func init() {
	if os.Getenv("MAGPIE_TEST_PANEL_CLOSE") != "1" {
		return
	}
	app := application.New(application.Options{
		Name: "magpie panel close regression",
		Mac:  application.MacOptions{ActivationPolicy: application.ActivationPolicyProhibited},
		Assets: application.AssetOptions{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<p>magpie</p>`)
		})},
	})
	h := &host{app: app}
	fail := func(format string, a ...any) {
		fmt.Printf("panel close: "+format+"\n", a...)
		os.Exit(1)
	}
	waitFor := func(what string, ok func() bool) {
		for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				fail("timed out waiting until %s", what)
			}
		}
	}
	visible := func(w *application.WebviewWindow) func() bool {
		return func() bool { return application.InvokeSyncWithResult(w.IsVisible) }
	}
	alive := func() bool { _, ok := app.Window.GetByName("panel"); return ok }
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			var w *application.WebviewWindow
			application.InvokeSync(func() {
				w, _ = h.panelWin()
				w.Show()
			})
			waitFor("the panel shows", visible(w))

			w.Close() // the title bar's X
			waitFor("the closed panel is hidden", func() bool { return !visible(w)() })
			time.Sleep(300 * time.Millisecond) // Wails closes on a goroutine of its own
			if !alive() {
				fail("the panel closed for good; the tray icon can't open it again")
			}
			if application.InvokeSyncWithResult(func() bool { return h.panel != w }) {
				fail("the panel was replaced")
			}
			application.InvokeSync(func() { w.Show() }) // the icon clicked again
			waitFor("the panel shows again", visible(w))

			// lightweight mode lets it go: then it closes
			application.InvokeSync(func() { w.Hide(); h.panel = nil })
			w.Close()
			waitFor("the panel let go closes", func() bool { return !alive() })
			fmt.Println("panel close: ok")
			app.Quit()
		}()
	})
	if err := app.Run(); err != nil {
		panic(err)
	}
	os.Exit(0)
}
