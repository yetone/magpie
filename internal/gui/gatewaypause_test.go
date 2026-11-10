package gui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// pauseSandbox gives a test its own HOME, a gateway port of its own and
// fake agents: wired says which are connected, and the hooks' disconnects
// and connects change it and are counted. The gateway is served before
// the test and stopped after it.
func pauseSandbox(t *testing.T, ids ...string) (port int, wired map[string]bool, calls *[]string) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("APPDATA", filepath.Join(h, "AppData"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	port = freePort(t)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:"+strconv.Itoa(port))
	was := pausable.Load()
	pausable.Store(true)
	agents := map[string]*agent.Agent{}
	wired = map[string]bool{}
	for _, id := range ids {
		agents[id] = &agent.Agent{ID: id, Name: strings.ToUpper(id)}
		wired[id] = true
	}
	calls = &[]string{}
	hooks := []any{pauseWired, pauseIsWired, pauseDisconnect, pauseConnect, pauseFind, onPaused}
	pauseWired = func() []*agent.Agent {
		var out []*agent.Agent
		for _, id := range ids {
			if wired[id] {
				out = append(out, agents[id])
			}
		}
		return out
	}
	pauseIsWired = func(a *agent.Agent) bool { return wired[a.ID] }
	pauseDisconnect = func(a *agent.Agent) error {
		*calls = append(*calls, "off "+a.ID)
		wired[a.ID] = false
		return nil
	}
	pauseConnect = func(a *agent.Agent) error {
		*calls = append(*calls, "on "+a.ID)
		wired[a.ID] = true
		return nil
	}
	pauseFind = func(id string) (*agent.Agent, error) {
		if a := agents[id]; a != nil {
			return a, nil
		}
		return nil, errors.New("no agent " + id)
	}
	onPaused = nil
	served.Store(nil)
	t.Cleanup(func() {
		gatewayMu.Lock()
		stopServedLocked()
		gatewayMu.Unlock()
		pausable.Store(was)
		pauseWired = hooks[0].(func() []*agent.Agent)
		pauseIsWired = hooks[1].(func(*agent.Agent) bool)
		pauseDisconnect = hooks[2].(func(*agent.Agent) error)
		pauseConnect = hooks[3].(func(*agent.Agent) error)
		pauseFind = hooks[4].(func(string) (*agent.Agent, error))
		onPaused = hooks[5].(func())
	})
	gatewayMu.Lock()
	out := startHere()
	gatewayMu.Unlock()
	if !out.OK || !answers(port) {
		t.Fatalf("gateway not served: %+v", out)
	}
	return port, wired, calls
}

// me2seeks on X: quitting magpie with Disconnect and quit was the only way
// to stop its gateway. Pause disconnects every connected agent and stops
// the listener with the app still running; nothing serves it meanwhile;
// Resume serves it again and connects those agents again.
func TestGatewayPauseAndResume(t *testing.T) {
	port, wired, calls := pauseSandbox(t, "codex", "claude")
	told := 0
	onPaused = func() { told++ }

	out := pauseGateway()
	if !out.Paused || out.Error != "" || !slices.Equal(out.Agents, []string{"codex", "claude"}) {
		t.Fatalf("pause: %+v", out)
	}
	if wired["codex"] || wired["claude"] {
		t.Fatalf("agents still connected: %v", wired)
	}
	if served.Load() != nil || answers(port) {
		t.Fatal("the gateway still serves while paused")
	}
	if _, err := os.Stat(pausePath()); err != nil {
		t.Fatalf("the pause isn't kept: %v", err)
	}
	if !providersState().Gateway.Paused || !settingsState().GatewayPaused {
		t.Fatal("the pages aren't told of the pause")
	}
	if l := trayMenuLabels("en", "1", "", pausedNow()); l.pause != "Resume Gateway" {
		t.Fatalf("tray reads %q", l.pause)
	}
	// nothing starts it meanwhile: the watch for a gateway gone, a port
	// change, a handover
	gatewayMu.Lock()
	gw := serveGatewayLocked()
	gatewayMu.Unlock()
	if gw != nil || served.Load() != nil || answers(port) {
		t.Fatal("served while paused")
	}
	// a second pause changes nothing
	if again := pauseGateway(); !again.Paused || len(*calls) != 2 {
		t.Fatalf("pause again: %+v, %v", again, *calls)
	}

	// the Gateway page's Start resumes
	mux := http.NewServeMux()
	gatewayFixRoutes(mux)
	gatewayPauseRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/gateway/restart", strings.NewReader("{}")))
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("restart while paused: %s", rec.Body)
	}
	if pausedNow() || served.Load() == nil || !answers(port) {
		t.Fatal("not resumed")
	}
	if !wired["codex"] || !wired["claude"] {
		t.Fatalf("agents not connected again: %v", wired)
	}
	if !slices.Equal(*calls, []string{"off codex", "off claude", "on codex", "on claude"}) {
		t.Fatalf("calls: %v", *calls)
	}
	if told != 2 {
		t.Fatalf("the tray was told %d times", told)
	}

	// and the route pauses and resumes
	for _, on := range []string{"true", "false"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/gateway/pause", strings.NewReader(`{"on":`+on+`}`)))
		if rec.Code != 200 || pausedNow() != (on == "true") || answers(port) == (on == "true") {
			t.Fatalf("pause %s: %d %s", on, rec.Code, rec.Body)
		}
	}
}

// An agent that can't be disconnected leaves the gateway serving, and those
// disconnected before it are connected again: a pause never leaves an
// agent pointed at a gateway that isn't there.
func TestGatewayPauseRollsBack(t *testing.T) {
	port, wired, calls := pauseSandbox(t, "codex", "claude")
	off := pauseDisconnect
	pauseDisconnect = func(a *agent.Agent) error {
		if a.ID == "claude" {
			return errors.New("config is read-only")
		}
		return off(a)
	}
	out := pauseGateway()
	if out.Paused || out.Agent != "CLAUDE" || out.Error != "config is read-only" {
		t.Fatalf("pause: %+v", out)
	}
	if pausedNow() || served.Load() == nil || !answers(port) {
		t.Fatal("the gateway stopped though an agent stayed on it")
	}
	if !wired["codex"] || !slices.Equal(*calls, []string{"off codex", "on codex"}) {
		t.Fatalf("codex not connected again: %v %v", wired, *calls)
	}
}

// Connecting an agent while paused resumes: it would otherwise point at a
// gateway that isn't there.
func TestGatewayResumesOnWire(t *testing.T) {
	port, wired, _ := pauseSandbox(t, "codex")
	if out := pauseGateway(); !out.Paused {
		t.Fatalf("pause: %+v", out)
	}
	a, _ := pauseFind("codex")
	resumeOnWire(a) // not connected: stays paused
	if !pausedNow() {
		t.Fatal("resumed with no agent connected")
	}
	wired["codex"] = true
	resumeOnWire(a)
	if pausedNow() || !answers(port) {
		t.Fatal("not resumed once an agent was connected")
	}
}

// A pause file that can't be read is a pause, not a gateway to start; a
// process that isn't the app (magpie web, the CLI) is never paused.
func TestGatewayPauseFileUnreadable(t *testing.T) {
	pauseSandbox(t)
	if err := os.MkdirAll(filepath.Dir(pausePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pausePath(), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, ids := gatewayPaused(); !p || ids != nil {
		t.Fatalf("half-written file: %v %v", p, ids)
	}
	pausable.Store(false)
	if pausedNow() {
		t.Fatal("paused outside the app")
	}
}
