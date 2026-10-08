package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/settings"
)

// Gateway mode (Player on Discord): `magpie web` on a server that is only
// the gateway for other machines' agents shows what such a gateway needs —
// Providers, Gateway, Routing, Usage, Sessions, Plugins and Settings — and leaves out
// what is this machine's agents' (Agents, Library, the settings
// written into agents' files) and its desktop's (the tray, the Dock, open
// at login, notifications). It is the browser page's alone: the app's
// windows show everything whatever it says.
//
// Settings' choice (settings.GatewayMode, "on" or "off") wins; with none,
// `magpie web --gateway` turns it on, and so does a magpie web with no
// agent on its machine (a container, a NAS). Settings › General turns it
// off again, which brings the pages back.

// WebGateway is `magpie web --gateway`'s.
var WebGateway atomic.Bool

// webPage is set once the page is served by `magpie web`: settingsState,
// which has no Windows, tells the page its gateway mode by it.
var webPage atomic.Bool

// gatewayMode says whether the page is in gateway mode, and why: "on" or
// "off" (Settings' choice), "flag" (magpie web --gateway), "no-agents", or
// "" (the app's windows, or magpie web on a machine with agents).
func gatewayMode(web bool) (bool, string) {
	if !web {
		return false, ""
	}
	switch m := settings.Load().GatewayMode; m {
	case "on", "off":
		return m == "on", m
	}
	if WebGateway.Load() {
		return true, "flag"
	}
	if !agentsHere() {
		return true, "no-agents"
	}
	return false, ""
}

// agentsHere says whether an agent is found on this computer; a test's own.
var agentsHere = func() bool { return len(agent.Detected()) > 0 }

// gatewayModeRoutes: POST /api/settings/gateway-mode {"mode": "on" | "off"
// | ""} sets Settings' choice, "" leaving it to magpie again.
func gatewayModeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/settings/gateway-mode", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in.Mode != "" && in.Mode != "on" && in.Mode != "off" {
			fail(rw, fmt.Errorf("gateway mode is on, off or \"\" (automatic), not %q", in.Mode))
			return
		}
		s := settings.Load()
		s.GatewayMode = in.Mode
		if err := settings.Save(s); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
}
