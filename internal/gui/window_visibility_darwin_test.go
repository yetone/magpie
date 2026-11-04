//go:build darwin && cgo && !nogui

package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// A closed NSWindow keeps its WKWebView's document visible unless magpie
// says otherwise. The page here records what it saw as it was hidden, and
// reports that record once it is shown again; without hideWebview's signal
// it remains a visible page with running animations.
func TestClosingWindowMarksPageOutOfSight(t *testing.T) {
	out, err := runAppKit(t, 30*time.Second, []string{"MAGPIE_TEST_WINDOW_VISIBILITY=1"})
	if err != nil || !strings.Contains(string(out), "window visibility: ok") {
		t.Fatalf("window visibility: %v\n%s", err, out)
	}
}

// The tray panel is hidden by Wails itself more often than by magpie: on a
// click elsewhere (HideOnFocusLost) and on Escape, a plain Hide that
// hideWebview never sees. Its page is told all the same, or it is never
// told it is back and keeps what it was drawn with: Settings' currency and
// number units never reached it (blrbrzkj on Discord).
func TestPanelHiddenByWailsMarksPageOutOfSight(t *testing.T) {
	out, err := runAppKit(t, 30*time.Second, []string{"MAGPIE_TEST_WINDOW_VISIBILITY=panel"})
	if err != nil || !strings.Contains(string(out), "window visibility: ok") {
		t.Fatalf("panel visibility: %v\n%s", err, out)
	}
}

func init() {
	which := os.Getenv("MAGPIE_TEST_WINDOW_VISIBILITY")
	if which != "1" && which != "panel" {
		return
	}
	ready := make(chan struct{}, 1)
	reports := make(chan url.Values, 1)
	app := application.New(application.Options{
		Name: "magpie window visibility regression",
		Mac:  application.MacOptions{ActivationPolicy: application.ActivationPolicyProhibited},
		Assets: application.AssetOptions{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/":
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, `<!doctype html><script type="module">import "/wails/runtime.js";</script><style>
#probe{animation:spin 1s linear infinite}@keyframes spin{to{transform:rotate(1turn)}}
html.magpie-out-of-sight #probe{animation-play-state:paused!important}
</style><i id="probe"></i><script>
const probe = document.querySelector("#probe");
window.magpieWindowVisibility = (visible) => {
  // shown again: what it last saw while hidden, once
  const seen = visible && localStorage.getItem("hiddenProbe");
  if (seen) {
    localStorage.removeItem("hiddenProbe");
    fetch("/report?probe=" + encodeURIComponent(seen));
  }
  document.documentElement.classList.toggle("magpie-out-of-sight", !visible);
  document.dispatchEvent(new Event("visibilitychange"));
};
document.addEventListener("visibilitychange", () => {
  if (document.hidden) {
    localStorage.setItem("hiddenProbe", JSON.stringify({
      hidden: document.hidden,
      state: document.visibilityState,
      marked: document.documentElement.classList.contains("magpie-out-of-sight"),
      play: getComputedStyle(probe).animationPlayState,
    }));
  }
});
fetch("/ready");
</script>`)
			case "/ready":
				select {
				case ready <- struct{}{}:
				default:
				}
				w.WriteHeader(http.StatusNoContent)
			case "/report":
				select {
				case reports <- r.URL.Query():
				default:
				}
				w.WriteHeader(http.StatusNoContent)
			default:
				http.NotFound(w, r)
			}
		})},
	})
	h := &host{app: app}
	fail := func(format string, a ...any) {
		fmt.Printf("window visibility: "+format+"\n", a...)
		os.Exit(1)
	}
	wait := func(what string, ch <-chan url.Values) url.Values {
		select {
		case v := <-ch:
			return v
		case <-time.After(8 * time.Second):
			fail("timed out waiting for %s", what)
			return nil
		}
	}
	// each way the window is closed: the main window by magpie; the panel
	// by Wails, on a click elsewhere and on Escape
	closes := map[string][]func(*host, *application.WebviewWindow){
		"1": {func(h *host, _ *application.WebviewWindow) { application.InvokeSync(h.hideMain) }},
		"panel": {
			func(_ *host, w *application.WebviewWindow) { w.HandleWindowEvent(uint(events.Common.WindowLostFocus)) },
			func(_ *host, w *application.WebviewWindow) { w.HandleKeyEvent("escape") },
		},
	}
	check := func(report url.Values) {
		probe := struct {
			Hidden bool   `json:"hidden"`
			State  string `json:"state"`
			Marked bool   `json:"marked"`
			Play   string `json:"play"`
		}{}
		if err := json.Unmarshal([]byte(report.Get("probe")), &probe); err != nil {
			fail("the page's hidden event is %q: %v", report.Get("probe"), err)
		}
		if !probe.Hidden || probe.State != "hidden" || !probe.Marked || probe.Play != "paused" {
			fail("while hidden the page saw hidden=%v state=%q marked=%v play=%q", probe.Hidden, probe.State, probe.Marked, probe.Play)
		}
	}
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			var win *application.WebviewWindow
			application.InvokeSync(func() {
				if which == "panel" {
					win = h.makePanel()
					h.panel = win
					win.Show()
					return
				}
				win = app.Window.NewWithOptions(application.WebviewWindowOptions{
					Name:   "visibility",
					Title:  "visibility",
					URL:    "/",
					Width:  480,
					Height: 320,
				})
				h.main = win
			})
			select {
			case <-ready:
			case <-time.After(8 * time.Second):
				fail("timed out waiting for the page")
			}

			// the panel by a click elsewhere, then by Escape
			for _, hide := range closes[which] {
				hide(h, win)
				time.Sleep(500 * time.Millisecond)
				application.InvokeSync(func() { showWebview(win).Focus() })
				check(wait("the page's hidden report", reports))
			}
			fmt.Println("window visibility: ok")
			app.Quit()
		}()
	})
	if err := app.Run(); err != nil {
		panic(err)
	}
	os.Exit(0)
}
