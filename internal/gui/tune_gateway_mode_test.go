package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
)

// linkaww on Discord, a magpie in Docker in gateway mode: Usage › Context's
// Tuned for you said Claude Code made 0 calls, while Through magpie below
// counted its requests. Tuned for you replays this computer's agents'
// session files and changes their settings; in gateway mode the agents are
// on other computers, so it has nothing to replay and nothing of theirs to
// change. /api/tune says so and gives no card, and Use and Undo are
// refused. Gateway mode off, as before.
func TestTuneInGatewayModeIsLeftOut(t *testing.T) {
	h := sandboxHome(t)
	tuneForget()
	t.Cleanup(func() {
		webPage.Store(false)
		ownWas.Store(0)
		tuneForget()
	})
	settingsPath := filepath.Join(h, ".claude", "settings.json")
	if err := edit.SetJSON(settingsPath, edit.KV{Path: "promptCacheTtl", Value: "1h"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	tuneRoutes(mux)
	gatewayModeRoutes(mux)
	call := func(method, path, body string) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w.Code, w.Body.String()
	}
	tuned := func() tuneState {
		t.Helper()
		code, b := call("GET", "/api/tune?days=7", "")
		var st tuneState
		if err := json.Unmarshal([]byte(b), &st); code != 200 || err != nil {
			t.Fatalf("GET /api/tune: %d %s %v", code, b, err)
		}
		return st
	}
	if st := tuned(); st.Gateway || len(st.Agents) != 1 || st.Agents[0].Agent != "claude" {
		t.Fatalf("gateway mode off: %+v; want Claude Code's card", st)
	}

	webPage.Store(true)
	if code, b := call("POST", "/api/settings/gateway-mode", `{"mode":"on"}`); code != 200 {
		t.Fatalf("gateway mode on: %d %s", code, b)
	}
	if st := tuned(); !st.Gateway || len(st.Agents) != 0 || st.Days != 7 {
		t.Fatalf("gateway mode: %+v; want no card, said to be gateway mode", st)
	}
	use := `{"agent":"claude","knob":"` + agent.TuneCache + `","value":"5m"}`
	for _, path := range []string{"/api/tune/apply", "/api/tune/undo"} {
		if code, b := call("POST", path, use); code != http.StatusConflict {
			t.Fatalf("gateway mode: %s: %d %s; want it refused", path, code, b)
		}
	}
	if v, _ := edit.GetJSON(settingsPath, "promptCacheTtl"); v != `"1h"` && v != "1h" {
		t.Fatalf("gateway mode: promptCacheTtl %q; want it left at 1h", v)
	}
}
