package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/sessions"
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
		// the Sessions page lists this computer's agents' sessions, or
		// stops, at once (ownSessionsOff)
		ownSessionsOff()
		forgetOwnSessions()
		writeJSON(rw, settingsState())
	})
}

// ownSessionsOff is gatewayMode as the sessions' gate asks it
// (sessions.OwnOff; paultian on Discord): in gateway mode the Sessions page
// and Usage › Sessions list what came through this gateway, never this
// computer's own agents' session files, which a machine put in gateway
// mode with agents installed still listed. It is asked on every read of a
// session, so it reads the shared settings and an agents look kept a while
// (agentsHereKept) rather than gatewayMode's own. When its answer turns,
// what was read under the other answer is dropped (ownTurned).
func ownSessionsOff() bool {
	on := false
	if webPage.Load() {
		switch settings.Shared().GatewayMode {
		case "on":
			on = true
		case "off":
		default:
			on = WebGateway.Load() || !agentsHereKept()
		}
	}
	ownTurned(on)
	return on
}

// ownWas is ownSessionsOff's last answer: 0 none yet, 1 off, 2 on.
var ownWas atomic.Int32

// ownTurned drops the sessions read, and the stats made, under the other
// answer, off the caller's goroutine: the gate is asked under the
// sessions' own lock, which Reset takes.
func ownTurned(on bool) {
	v := int32(1)
	if on {
		v = 2
	}
	if was := ownWas.Swap(v); was != 0 && was != v {
		go forgetOwnSessions()
	}
}

func forgetOwnSessions() {
	sessions.Reset()
	forgetStats()
}

// agentsHereKept is agentsHere looked at most every half minute: it looks
// for every agent's files and commands. Another HOME or PATH (a test's
// sandbox) is looked at afresh.
var agentsSeen struct {
	sync.Mutex
	at   time.Time
	on   string // the HOME and PATH it was looked for in
	here bool
}

func agentsHereKept() bool {
	agentsSeen.Lock()
	defer agentsSeen.Unlock()
	on := os.Getenv("HOME") + "\x00" + os.Getenv("PATH")
	if agentsSeen.at.IsZero() || agentsSeen.on != on || time.Since(agentsSeen.at) > 30*time.Second {
		agentsSeen.here, agentsSeen.at, agentsSeen.on = agentsHere(), time.Now(), on
	}
	return agentsSeen.here
}

func init() {
	f := ownSessionsOff
	sessions.OwnOff.Store(&f)
}
