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

// A click on the Dock icon with the window open on another Space goes to
// that Space; one closed or minimised opens on the user's (#1252). A test
// can't make or switch Spaces, so it checks what decides them: an open
// window doesn't move to the active Space when magpie is activated, and one
// shown from closed or minimised does, while it is shown.
func TestDockReopenKeepsTheWindowsSpace(t *testing.T) {
	out, err := runAppKit(t, 30*time.Second, []string{"MAGPIE_TEST_DOCK_REOPEN=1"})
	if err != nil || !strings.Contains(string(out), "dock reopen: ok") {
		t.Fatalf("dock reopen: %v\n%s", err, out)
	}
}

func init() {
	if os.Getenv("MAGPIE_TEST_DOCK_REOPEN") != "1" {
		return
	}
	app := application.New(application.Options{
		Name: "magpie dock reopen regression",
		Mac:  application.MacOptions{ActivationPolicy: application.ActivationPolicyAccessory},
		Assets: application.AssetOptions{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<p>magpie</p>`)
		})},
	})
	h := &host{app: app}
	fail := func(format string, a ...any) {
		fmt.Printf("dock reopen: "+format+"\n", a...)
		os.Exit(1)
	}
	// state is the window's, read on the main thread
	state := func() (open, moves, minimised bool) {
		application.InvokeSync(func() {
			open, moves, minimised = windowOpen(h.main), movesToActiveSpace(h.main), h.main.IsMinimised()
		})
		return
	}
	waitFor := func(what string, ok func() bool) {
		for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				fail("timed out waiting until %s", what)
			}
		}
	}
	settled := func() bool { _, moves, _ := state(); return !moves }
	// shownHere: the window was just shown from closed or minimised, on the
	// user's Space, and stays on it once shown
	shownHere := func(how string) {
		if open, moves, _ := state(); !moves {
			fail("%s: the window wasn't moved to the active Space as it was shown (open=%v)", how, open)
		}
		waitFor(how+": the window is open", func() bool { open, _, min := state(); return open && !min })
		waitFor(how+": the shown window stays on its Space", settled)
	}
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			// first opened, as at start or after lightweight mode let it go
			application.InvokeSync(func() { h.openMain("") })
			shownHere("opened")

			// open: activated where it is, never moved
			application.InvokeSync(h.reopenMain)
			if open, moves, _ := state(); !open || moves {
				fail("reopened while open: open=%v, moves to the active Space=%v", open, moves)
			}

			// closed
			h.hideMain()
			waitFor("the window is closed", func() bool { open, _, _ := state(); return !open })
			application.InvokeSync(h.reopenMain)
			shownHere("reopened after closing")

			// minimised
			application.InvokeSync(func() { h.main.Minimise() })
			waitFor("the window is minimised", func() bool { _, _, min := state(); return min })
			if open, _, _ := state(); open {
				fail("a minimised window counts as open")
			}
			application.InvokeSync(h.reopenMain)
			shownHere("reopened after minimising")

			fmt.Println("dock reopen: ok")
			app.Quit()
		}()
	})
	if err := app.Run(); err != nil {
		panic(err)
	}
	os.Exit(0)
}
