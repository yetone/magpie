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

// #2141: with Chrome full screen, a click on the menu bar icon showed the
// quick panel on the desktop Space it was first shown on, out of sight. A
// test can't make a full-screen Space, so it checks what decides it on the
// real window: the panel, made at start and made again after lightweight
// mode let it go, is on every Space and over a full-screen app's, and is
// shown and hidden with that set, without activating magpie (activated,
// the Mac took the user to the Space of magpie's other windows). Placed by
// the tray icon, it is at a menu bar extra's level, not the pop-up menu
// level Wails' PositionWindow gives it, where WeChat's screenshot couldn't
// draw over it (zyh6036 on X).
func TestPanelJoinsFullScreenSpaces(t *testing.T) {
	out, err := runAppKit(t, 30*time.Second, []string{"MAGPIE_TEST_PANEL_SPACE=1"})
	if err != nil || !strings.Contains(string(out), "panel space: ok") {
		t.Fatalf("panel space: %v\n%s", err, out)
	}
}

func init() {
	if os.Getenv("MAGPIE_TEST_PANEL_SPACE") != "1" {
		return
	}
	app := application.New(application.Options{
		Name: "magpie panel space regression",
		Mac:  application.MacOptions{ActivationPolicy: application.ActivationPolicyAccessory},
		Assets: application.AssetOptions{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<p>magpie</p>`)
		})},
	})
	h := &host{app: app, tray: app.SystemTray.New()}
	fail := func(format string, a ...any) {
		fmt.Printf("panel space: "+format+"\n", a...)
		os.Exit(1)
	}
	waitFor := func(what string, ok func() bool) {
		for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				fail("timed out waiting until %s", what)
			}
		}
	}
	check := func(how string) {
		var w *application.WebviewWindow
		application.InvokeSync(func() { w, _ = h.panelWin() })
		if !application.InvokeSyncWithResult(func() bool { return joinsFullScreenSpaces(w) }) {
			fail("%s: the panel isn't on every Space and over full-screen ones", how)
		}
		if !application.InvokeSyncWithResult(func() bool { return nonActivating(w) }) {
			fail("%s: the panel isn't a non-activating panel", how)
		}
		if application.InvokeSyncWithResult(appFrontmost) {
			fail("%s: magpie is frontmost before the icon is clicked; the test can't tell", how)
		}
		application.InvokeSync(func() { h.placeByIcon(w); w.Show().Focus() }) // the icon clicked
		if !application.InvokeSyncWithResult(func() bool { return panelLevelOK(w) }) {
			fail("%s: placed by the icon, the panel is at level %d, not a menu bar extra's (NSStatusWindowLevel), so a screenshot tool's overlay can't cover it", how, application.InvokeSyncWithResult(func() int { return panelLevel(w) }))
		}
		waitFor(how+": the panel shows", func() bool { return application.InvokeSyncWithResult(func() bool { return windowOpen(w) }) })
		// activation is asked for and comes a moment later: give it time
		time.Sleep(500 * time.Millisecond)
		if application.InvokeSyncWithResult(appFrontmost) {
			fail("%s: showing the panel brought magpie to the front, which takes the user to the Space of its other windows", how)
		}
		if !application.InvokeSyncWithResult(func() bool { return canBeKey(w) }) {
			fail("%s: the panel can't be made key, so it never takes the keyboard", how)
		}
		// Whether it is key is up to the session too: on a Mac no one was
		// at, macOS 26.2 made no non-activating panel of an app that isn't
		// active key, a plain titled one either (#2450). A click on the
		// icon is input, which this test can't make, so where a plain one
		// isn't made key the check can't tell.
		if !application.InvokeSyncWithResult(w.IsFocused) {
			if application.InvokeSyncWithResult(sessionKeysPanels) {
				fail("%s: the panel shown doesn't take the keyboard", how)
			}
			fmt.Printf("panel space: %s: not checked that it takes the keyboard: this session makes no non-activating panel key\n", how)
		}
		if !application.InvokeSyncWithResult(func() bool { return joinsFullScreenSpaces(w) }) {
			fail("%s: showing the panel took it off full-screen Spaces", how)
		}
		application.InvokeSync(func() { w.Hide() })
		waitFor(how+": the panel hides", func() bool { return !application.InvokeSyncWithResult(func() bool { return windowOpen(w) }) })
	}
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			check("made at start")
			application.InvokeSync(func() { h.panel = nil }) // lightweight mode let it go
			check("made again")
			fmt.Println("panel space: ok")
			app.Quit()
		}()
	})
	if err := app.Run(); err != nil {
		panic(err)
	}
	os.Exit(0)
}
