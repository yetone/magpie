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

// nb5p on Discord (macOS 26): Ice put magpie's menu bar icon back in its
// hidden section after every update, and after a plain quit and reopen,
// however often it was moved out. AppKit keeps where the item sits by its
// autosave name ("NSStatusItem Preferred Position Item-0"), and Wails
// removes the item as the app quits, which forgets it: the next magpie's
// item started at the default spot. Renamed first, the removal forgets
// nothing kept.
func TestQuitKeepsTheMenuBarIconsPlace(t *testing.T) {
	for _, c := range []struct{ keep, want string }{
		{"0", "tray place: forgotten"}, // the cause, without the fix
		{"1", "tray place: kept"},
		// imty42 on X, #3873: the magpie handing over to the new version
		// hides its item, and setVisible:NO forgets the place the new
		// version's Item-0 shares
		{"handover", "tray place: kept"},
	} {
		out, err := runAppKit(t, 30*time.Second, []string{"MAGPIE_TEST_TRAY_PLACE=" + c.keep})
		if err != nil || !strings.Contains(string(out), c.want) {
			t.Fatalf("keep %s: want %q: %v\n%s", c.keep, c.want, err, out)
		}
	}
}

func init() {
	keep := os.Getenv("MAGPIE_TEST_TRAY_PLACE")
	if keep == "" {
		return
	}
	fail := func(format string, a ...any) {
		fmt.Printf("tray place: "+format+"\n", a...)
		os.Exit(1)
	}
	// where the user put it, the launch before
	setTrayPlace("Item-0", 400)
	app := application.New(application.Options{
		Name: "magpie tray place regression",
		Mac:  application.MacOptions{ActivationPolicy: application.ActivationPolicyAccessory},
		Assets: application.AssetOptions{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<p>magpie</p>`)
		})},
	})
	tray := app.SystemTray.New()
	tray.SetTemplateIcon(trayIcon)
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			for deadline := time.Now().Add(5 * time.Second); trayItems() == 0; {
				if time.Now().After(deadline) {
					fail("no status item came up")
				}
				time.Sleep(50 * time.Millisecond)
			}
			// AppKit may move it to where the item really is; kept is
			// what counts
			if _, ok := trayPlace("Item-0"); !ok {
				fail("the place set before the start is gone")
			}
			if keep == "handover" {
				h := &host{app: app, tray: tray}
				h.leaveMenuBar()
				mainSettled()
				mainSettled() // the hidden visibility forgotten after
				if x, ok := trayPlace("Item-0"); !ok {
					fmt.Println("tray place: forgotten")
				} else if n := trayHidden(); n != 0 {
					fail("handing over kept %d items hidden", n)
				} else {
					fmt.Printf("tray place: kept (%v)\n", x)
				}
				os.Exit(0)
			}
			// what the app's shutdown does: magpie's task, then Wails'
			// removal of every tray
			if keep == "1" {
				if n := keepTrayPlace(); n != 1 {
					fail("kept the place of %d items, not 1", n)
				}
			}
			tray.Destroy()
			mainSettled()
			if x, ok := trayPlace("Item-0"); ok {
				fmt.Printf("tray place: kept (%v)\n", x)
			} else {
				fmt.Println("tray place: forgotten")
			}
			os.Exit(0)
		}()
	})
	if err := app.Run(); err != nil {
		fail("run: %v", err)
	}
	os.Exit(1)
}
