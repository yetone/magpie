package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// Gateway mode (Player on Discord): magpie web with no agents on its
// machine is in it by itself, and so is one started with --gateway;
// Settings' On or Off wins over both, and the Settings page's own save
// leaves it as it is. The app's windows are never in it. The page is told
// before it paints (boot.js), and the settings say why.
func TestGatewayMode(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	here := false
	was := agentsHere
	agentsHere = func() bool { return here }
	t.Cleanup(func() { agentsHere = was; WebGateway.Store(false); webPage.Store(false) })

	srv := webPageHandler(t, webHost{func() {}}, nil)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	type state struct {
		GatewayMode string
		GatewayOn   bool
		GatewayWhy  string
		Web         bool
	}
	check := func(when string, on bool, why string) {
		t.Helper()
		var s state
		if rec := do("GET", "/api/settings", ""); rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &s) != nil {
			t.Fatalf("%s: settings %d %s", when, rec.Code, rec.Body)
		}
		if !s.Web || s.GatewayOn != on || s.GatewayWhy != why {
			t.Fatalf("%s: settings say %+v, want on=%v why=%q", when, s, on, why)
		}
		boot := do("GET", "/boot.js", "").Body.String()
		if strings.Contains(boot, `"gateway":true`) != on {
			t.Fatalf("%s: boot.js %s", when, boot)
		}
	}
	set := func(mode string) {
		t.Helper()
		if rec := do("POST", "/api/settings/gateway-mode", `{"mode":"`+mode+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("set %q: %d %s", mode, rec.Code, rec.Body)
		}
	}

	check("no agents here", true, "no-agents")
	here = true
	check("an agent here", false, "")
	WebGateway.Store(true)
	check("--gateway", true, "flag")
	set("off")
	check("turned off with --gateway", false, "off")
	if settings.Load().GatewayMode != "off" {
		t.Fatalf("saved %q", settings.Load().GatewayMode)
	}
	// the Settings page's save, which sends what it was drawn with, leaves it
	if rec := do("POST", "/api/settings", `{"theme":"dark","lang":"en","gatewayMode":"on"}`); rec.Code != http.StatusOK {
		t.Fatalf("settings save: %d %s", rec.Code, rec.Body)
	}
	check("after the Settings page's save", false, "off")
	WebGateway.Store(false)
	set("on")
	check("turned on with agents here", true, "on")
	set("")
	check("automatic again", false, "")
	if rec := do("POST", "/api/settings/gateway-mode", `{"mode":"maybe"}`); rec.Code == http.StatusOK || settings.Load().GatewayMode != "" {
		t.Fatalf("a mode not offered was taken: %d", rec.Code)
	}

	// the app's windows: never, whatever Settings says
	set("on")
	rec := httptest.NewRecorder()
	Handler(&zoomWindows{}, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/boot.js", nil))
	if strings.Contains(rec.Body.String(), `"gateway":true`) {
		t.Fatalf("the app's window was told gateway mode: %s", rec.Body)
	}
	if on, _ := gatewayMode(false); on {
		t.Fatal("gatewayMode(false) is on")
	}

	// a sync or a restored backup never brings it from another computer
	in := settings.Settings{GatewayMode: "off"}
	in.KeepOwn(settings.Settings{GatewayMode: "on"})
	if in.GatewayMode != "on" {
		t.Fatalf("KeepOwn: %q", in.GatewayMode)
	}
}
