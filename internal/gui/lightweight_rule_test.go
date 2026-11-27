package gui

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// A closed window's webview goes on the second look in a row that finds it
// closed with lightweight mode on (#580), never while it is shown or the
// mode is off, and a window shown between looks starts over.
func TestLightStep(t *testing.T) {
	type look struct{ on, shown, release bool }
	for name, looks := range map[string][]look{
		"closed":         {{true, false, false}, {true, false, true}},
		"off":            {{false, false, false}, {false, false, false}, {false, false, false}},
		"shown":          {{true, true, false}, {true, true, false}, {true, true, false}},
		"shown between":  {{true, false, false}, {true, true, false}, {true, false, false}, {true, false, true}},
		"turned off":     {{true, false, false}, {false, false, false}, {true, false, false}, {true, false, true}},
		"again after it": {{true, false, false}, {true, false, true}, {true, false, false}, {true, false, true}},
	} {
		ticks := 0
		for i, l := range looks {
			var release bool
			ticks, release = lightStep(l.on, l.shown, ticks)
			if release != l.release {
				t.Fatalf("%s: look %d released %v", name, i, release)
			}
			if release {
				ticks = 0 // as lighten does once the window is let go
			}
		}
	}
}

// The Mac's tray panel is made when it is first opened, not at start: a
// hidden one kept a WebContent process of ~67 MB for a user who never
// opened it. Elsewhere it is still made at start.
func TestPanelOnFirstOpen(t *testing.T) {
	for goos, want := range map[string]bool{"darwin": true, "linux": false, "windows": false} {
		if got := panelOnFirstOpen(goos); got != want {
			t.Errorf("panelOnFirstOpen(%s) = %v, want %v", goos, got, want)
		}
	}
}

func TestMainOnFirstOpen(t *testing.T) {
	for _, c := range []struct {
		goos     string
		showMain bool
		want     bool
	}{
		{"darwin", false, true},
		{"darwin", true, false},
		{"linux", false, false},
		{"windows", false, false},
	} {
		if got := mainOnFirstOpen(c.goos, c.showMain); got != c.want {
			t.Errorf("mainOnFirstOpen(%q, %v) = %v, want %v", c.goos, c.showMain, got, c.want)
		}
	}
}

// The warm-up's read of the history starts with the app's page, never
// with the tray panel's page or a file the page loads.
func TestWarmsSessions(t *testing.T) {
	for _, c := range []struct {
		method, url string
		want        bool
	}{
		{"GET", "/?theme=dark", true},
		{"GET", "/", true},
		{"GET", "/index.html", true},
		{"GET", "/?mode=panel&theme=dark", false},
		{"GET", "/app.js", false},
		{"HEAD", "/", false},
	} {
		r := httptest.NewRequest(c.method, c.url, nil)
		if got := warmsSessions(r); got != c.want {
			t.Errorf("%s %s: %v, want %v", c.method, c.url, got, c.want)
		}
	}
}

// On Windows, WebView2's Embed runs a nested Win32 message pump while
// NewWithOptions is creating a window, so a tray double-click or repeated
// clicks can re-enter openMain before h.main is set. pendingMain keeps only
// one creation in flight, keeps the latest non-empty URL, and upgrades a
// quiet/minimised reveal when an active open arrives during creation.
func TestPendingMainCoalescesReentrantOpens(t *testing.T) {
	var p pendingMain
	if !p.begin("", true, true) {
		t.Fatal("first begin rejected")
	}
	if p.begin("/?view=settings", true, false) {
		t.Fatal("re-entrant begin was allowed to create a second window")
	}
	if p.begin("", false, false) {
		t.Fatal("second re-entrant begin was allowed to create a third window")
	}
	url, quiet, minimised := p.finish()
	if url != "/?view=settings" || quiet || minimised {
		t.Fatalf("finish = (%q, quiet=%v, minimised=%v), want (\"/?view=settings\", false, false)", url, quiet, minimised)
	}
	// After finish, a new creation is allowed again.
	if !p.begin("/?view=usage", true, true) {
		t.Fatal("begin after finish rejected")
	}
	// A later non-empty URL overrides an earlier pending one; cancel resets state.
	if p.begin("/?view=providers", true, true) {
		t.Fatal("re-entrant begin during second cycle was allowed")
	}
	p.cancel()
	if !p.begin("", false, false) {
		t.Fatal("begin after cancel rejected")
	}
	url, quiet, minimised = p.finish()
	if url != "" || quiet || minimised {
		t.Fatalf("finish after cancel = (%q, %v, %v), want (\"\", false, false)", url, quiet, minimised)
	}
}

func TestPendingRemoteCoalescesReentrantOpens(t *testing.T) {
	var p pendingRemote
	if !p.begin("http://host-a/#k1", "magpie · host-a") {
		t.Fatal("first begin rejected")
	}
	if p.begin("http://host-b/#k2", "magpie · host-b") {
		t.Fatal("re-entrant remote begin was allowed to create a second window")
	}
	link, title := p.finish()
	if link != "http://host-b/#k2" || title != "magpie · host-b" {
		t.Fatalf("finish = (%q, %q), want host-b", link, title)
	}
	p.cancel()
	if !p.begin("http://host-c/#k3", "magpie · host-c") {
		t.Fatal("begin after finish/cancel rejected")
	}
}

// A main window's WindowClosing hook only cancels and hides the window still
// held as h.main; any window marked gone or superseded by another h.main is
// allowed to close and destroy cleanly.
func TestMainClosingHidesOnlyCurrentMain(t *testing.T) {
	called := false
	if mainClosingHides(true, func() bool { called = true; return true }) {
		t.Fatal("a window marked gone was hidden instead of closed")
	}
	if called {
		t.Fatal("isMain was queried even though gone was already true")
	}
	if mainClosingHides(false, func() bool { return false }) {
		t.Fatal("a superseded/orphan main window was cancelled instead of closed")
	}
	if !mainClosingHides(false, func() bool { return true }) {
		t.Fatal("the current main window was not hidden on close")
	}
}

// Check that openMain, panelWin, OpenRemote, and makeMain's WindowClosing hook
// all wire the re-entrancy guards and current-main check.
func TestReentrantWindowCreationGuardsWired(t *testing.T) {
	lw, err := os.ReadFile("lightweight.go")
	if err != nil {
		t.Fatal(err)
	}
	lwSrc := string(lw)
	for _, want := range []string{
		"if !mainClosingHides(h.gone.Load() == w,",
		"if h.makingPanel {",
		"if !h.mainMake.begin(url, quiet, minimised) {",
	} {
		if !strings.Contains(lwSrc, want) {
			t.Errorf("lightweight.go missing %q", want)
		}
	}
	rem, err := os.ReadFile("remote_app.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rem), "if !h.remoteMake.begin(link, title) {") {
		t.Error("remote_app.go missing re-entrant creation guard")
	}
}
