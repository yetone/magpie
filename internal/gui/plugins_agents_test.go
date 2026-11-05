package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/agentplug"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// The Agents page's Install another agent says Plugins has more agents
// (the owner: 因为我们支持Agent插件了，这里是不是也应该显示插件中有更多？):
// /api/plugins/agents names the agents a suggested plugin adds, leaving
// out every other kind and one installed here, turned off or pinned to a
// version too.
func TestPluginAgentsNotInstalled(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"version":1,"plugins":[
			{"package":"@magpie-community/opencode-x-auth","name":"X","community":true},
			{"package":"@magpie-community/middleware-guard","name":"Guard","kind":"middleware","community":true},
			{"package":"@magpie-community/agent-kilo","name":"Kilo Code","kind":"agent","community":true},
			{"package":"@magpie-community/agent-aider","name":"Aider","kind":"agent","community":true},
			{"package":"@magpie-community/agent-pig","name":"PiG","kind":"agent","community":true},
			{"package":"@magpie-community/agent-bub","name":"Bub","kind":"agent","community":true},
			{"package":"agent-elsewhere","name":"Elsewhere","kind":"agent"}
		]}`))
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_PLUGIN_MARKET", srv.URL)
	plugin.RefreshMarket()
	t.Cleanup(plugin.RefreshMarket)
	dir := settings.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	installed := `{"plugins":[{"spec":"@magpie-community/agent-aider@0.1.3"},{"spec":"@magpie-community/agent-bub","off":true}]}`
	if err := os.WriteFile(filepath.Join(dir, "plugins.json"), []byte(installed), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	pluginRoutes(mux, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/plugins/agents", nil))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got []string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if want := []string{"Kilo Code", "PiG"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}

	// every one installed: the page gets an empty list, never null
	if err := os.WriteFile(filepath.Join(dir, "plugins.json"), []byte(`{"plugins":[{"spec":"@magpie-community/agent-kilo"},{"spec":"@magpie-community/agent-aider"},{"spec":"@magpie-community/agent-pig"},{"spec":"@magpie-community/agent-bub"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/plugins/agents", nil))
	if body := rec.Body.String(); body != "[]\n" && body != "[]" {
		t.Errorf("all installed: %q, want []", body)
	}
}

// A plugin's agent magpie doesn't find on this computer gets no row on
// Agents (zzZ: Kimi's plugin installed, Kimi not in Agents). Its row on
// Plugins says so, with where magpie looks and the plugin's own notice,
// where it said "Adds Kimi to Agents"; once its folder is there it is found.
func TestPluginAgentMissingSaysSo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", t.TempDir())
	dir := filepath.Join(home, "plugins", "kimi")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"kimi","magpie":{"agent":"./agent.js"}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "agent.js"), []byte(`
export const agent = { id: "kimi-desktop", name: "Kimi", config: "~/.config/magpie-kimi/connection.json", dir: "~/.config/magpie-kimi", model: "model", ua: ["Desktop Kimi Work"], notice: "Open Kimi first." };
export function connect({ gateway, model }) { return { base: gateway.v1, key: gateway.key }; }
`), 0o644)
	os.MkdirAll(settings.Dir(), 0o755)
	b, _ := json.Marshal(plugin.List{Plugins: []plugin.Entry{{Spec: dir}}})
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	agentplug.Reload()
	t.Cleanup(agentplug.Reload)

	got := pluginAgentOf(dir, nil)
	if got == nil || got.Name != "Kimi" {
		t.Fatalf("got %+v, want Kimi", got)
	}
	if !got.Missing || got.Notice != "Open Kimi first." || got.Looks != "~/.config/magpie-kimi" {
		t.Errorf("not on this computer: %+v, want missing, its notice and ~/.config/magpie-kimi", got)
	}

	if err := os.MkdirAll(filepath.Join(home, ".config", "magpie-kimi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := pluginAgentOf(dir, nil); got == nil || got.Missing || got.Notice != "" || got.Looks != "" {
		t.Errorf("found: %+v, want neither missing nor its notice", got)
	}
}
