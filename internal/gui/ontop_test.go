package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// pinHost is the app's host as the pin sees it: a window that can be
// kept on top, and what it was last told.
type pinHost struct {
	Windows
	on *[]bool
}

func (h pinHost) SetOnTop(on bool) { *h.on = append(*h.on, on) }

// tabPinHost is a browser's page in front of a host that could pin: the
// app's page served to a browser.
type tabPinHost struct{ pinHost }

func (tabPinHost) browserTab() {}

// The title bar's pin (NovaStudiosss on X) puts the main window on top at
// once and saves it, so the window is made pinned after a restart
// (makeMain reads settings.OnTop); unpinning does both back. The settings
// state says the pin can be used.
func TestPinKeepsWindowOnTop(t *testing.T) {
	sandboxHome(t)
	if !canOnTop(runtime.GOOS, func(string) string { return "" }) {
		t.Skip("no desktop that can pin here")
	}
	t.Setenv("XDG_SESSION_TYPE", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	var told []bool
	h := Handler(pinHost{on: &told}, nil)
	post := func(url string) {
		t.Helper()
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("POST", url, nil))
		if r.Code != 200 {
			t.Fatalf("%s: %d %s", url, r.Code, r.Body)
		}
		var out struct{ OnTop bool }
		if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if want := url == "/api/window/ontop?on=1"; out.OnTop != want {
			t.Fatalf("%s answered onTop %v", url, out.OnTop)
		}
	}
	post("/api/window/ontop?on=1")
	if len(told) != 1 || !told[0] {
		t.Fatalf("the window was told %v, want [true]", told)
	}
	if !settings.Load().OnTop {
		t.Fatal("the pin wasn't saved for the next start")
	}
	if boot := bootOf(t, h); boot["pin"] != true || boot["onTop"] != true {
		t.Fatalf("boot.js doesn't show the pin pressed: %v", boot)
	}
	post("/api/window/ontop?on=0")
	if len(told) != 2 || told[1] || settings.Load().OnTop {
		t.Fatalf("unpinned: told %v, saved %v", told, settings.Load().OnTop)
	}
	if boot := bootOf(t, h); boot["pin"] != true || boot["onTop"] != false {
		t.Fatalf("boot.js doesn't show the pin up: %v", boot)
	}
}

// bootOf is what boot.js tells the page.
func bootOf(t *testing.T, h http.Handler) map[string]any {
	t.Helper()
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/boot.js", nil))
	body := r.Body.String()
	i, j := strings.Index(body, "{"), strings.Index(body, "};")
	if i < 0 || j < i {
		t.Fatalf("boot.js: %.300s", body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body[i:j+1]), &out); err != nil {
		t.Fatalf("boot.js: %v in %.300s", err, body)
	}
	return out
}

// A browser's page (magpie web, or the app's page served to a browser)
// has no window of its own to pin, and a Wayland session can't keep one
// on top: the pin is refused there and nothing is saved.
func TestPinRefusedWithoutAWindowToPin(t *testing.T) {
	sandboxHome(t)
	var told []bool
	for name, w := range map[string]Windows{
		"magpie web":     webHost{},
		"no pin to make": folderOnly{},
		"browser tab":    tabPinHost{pinHost{on: &told}},
	} {
		r := httptest.NewRecorder()
		webPageHandler(t, w, nil).ServeHTTP(r, httptest.NewRequest("POST", "/api/window/ontop?on=1", nil))
		if r.Code != 400 {
			t.Fatalf("%s: %d %s, want refused", name, r.Code, r.Body)
		}
		if boot := bootOf(t, webPageHandler(t, w, nil)); boot["pin"] != nil {
			t.Fatalf("%s: boot.js shows the pin: %v", name, boot)
		}
	}
	if len(told) != 0 || settings.Load().OnTop {
		t.Fatalf("a refused pin reached the window (%v) or was saved (%v)", told, settings.Load().OnTop)
	}

	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		goos string
		env  map[string]string
		can  bool
	}{
		{"darwin", nil, true},
		{"windows", nil, true},
		{"linux", map[string]string{"XDG_SESSION_TYPE": "x11"}, true},
		{"linux", map[string]string{"XDG_SESSION_TYPE": "wayland"}, false},
		{"linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, false},
		{"linux", map[string]string{"XDG_SESSION_TYPE": "wayland", "GDK_BACKEND": "x11"}, true},
	} {
		if got := canOnTop(c.goos, env(c.env)); got != c.can {
			t.Errorf("canOnTop(%s, %v) = %v, want %v", c.goos, c.env, got, c.can)
		}
	}
}
