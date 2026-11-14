package gui

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/desktopdir"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// xMopf7RF on X, 0.1.4111: after Use Claude Magpie the Agents page lit a
// row named Claude Desktop, though only Claude Magpie was wired, and left
// "Claude Code · Claude Magpie" reading not connected. While Claude Magpie
// is on, the claude-desktop row (whose switch wires it alone) is named
// Claude Magpie, and once it is wired, its Code tab folder's row says it
// is on magpie through it (twinCode). The user's own Desktop isn't wired.
func TestClaudeMagpieRowsAfterUseClaudeMagpie(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	old := desktopdir.OS
	desktopdir.OS = "darwin"
	t.Cleanup(func() { desktopdir.OS = old })
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	// the user's own Desktop, signed in with Anthropic
	own := filepath.Join(home, "Library", "Application Support", "Claude")
	os.MkdirAll(own, 0o755)
	ownCfg := filepath.Join(own, "claude_desktop_config.json")
	os.WriteFile(ownCfg, []byte(`{"mcpServers":{}}`+"\n"), 0o600)
	// Claude Magpie as SetDesktopIsolated leaves it: on, its folder and its
	// Code tab's Claude Code folder made
	iso := desktopdir.Isolated("darwin", home)
	os.MkdirAll(iso, 0o755)
	code := filepath.Join(home, ".claude-magpie-desktop")
	os.MkdirAll(code, 0o700)
	os.WriteFile(filepath.Join(code, "settings.json"), []byte("{}\n"), 0o600)

	h := Handler(nil, nil)
	call := func(path, body string) stateJSON {
		t.Helper()
		method := "POST"
		if body == "" {
			method = "GET"
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%s %d %s", path, rec.Code, rec.Body)
		}
		var s stateJSON
		json.Unmarshal(rec.Body.Bytes(), &s)
		return s
	}
	row := func(s stateJSON, id string) agentJSON {
		t.Helper()
		for _, a := range s.Agents {
			if a.ID == id {
				return a
			}
		}
		t.Fatalf("no %s in %+v", id, s.Agents)
		return agentJSON{}
	}

	if a := row(call("/api/state", ""), "claude-desktop"); a.Name != "Claude Desktop" || a.Isolated == nil || a.Isolated.On {
		t.Fatalf("Claude Magpie off: %q %+v", a.Name, a.Isolated)
	}
	s := settings.Load()
	s.DesktopIsolated, s.DesktopIsolatedClaude = true, true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	st := call("/api/state", "")
	if a := row(st, "claude-desktop"); a.Name != "Claude Magpie" || a.Wired {
		t.Fatalf("Claude Magpie on: %q wired %v", a.Name, a.Wired)
	}
	if a := row(st, "claude#magpie-desktop"); a.TwinCode {
		t.Fatal("its Code tab on magpie before Claude Magpie is wired")
	}
	ownWas, _ := os.ReadFile(ownCfg)

	st = call("/api/agents/connect/claude-desktop", "{}")
	if a := row(st, "claude-desktop"); a.Name != "Claude Magpie" || !a.Wired {
		t.Fatalf("connected: %q wired %v", a.Name, a.Wired)
	}
	if a := row(st, "claude#magpie-desktop"); a.Wired || !a.TwinCode {
		t.Fatalf("its Code tab's row: wired %v, twinCode %v", a.Wired, a.TwinCode)
	}
	for _, a := range st.Agents {
		if a.TwinCode && a.ID != "claude#magpie-desktop" {
			t.Fatalf("%s said to be Claude Magpie's Code tab", a.ID)
		}
	}
	if b, _ := os.ReadFile(ownCfg); string(b) != string(ownWas) {
		t.Fatalf("the user's own Desktop changed:\n%s\nwas:\n%s", b, ownWas)
	}
	if _, err := os.Stat(filepath.Join(home, "Library", "Application Support", "Claude-3p")); err == nil {
		t.Fatal("the user's own Desktop's Claude-3p made")
	}
}
