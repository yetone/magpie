package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// clinePlugin is Cline's plugin signed in, its models as its feed lists
// them: ids with Cline's own prefixes and no window, as models.dev knows
// them under their makers.
func clinePlugin(t *testing.T) {
	t.Helper()
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", "")
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "zai":{"id":"zai","models":{"glm-5.3-flash":{"id":"glm-5.3-flash","limit":{"context":1000000,"output":131072}}}},
	  "xiaomi":{"id":"xiaomi","models":{"mimo-v2.6-flash":{"id":"mimo-v2.6-flash","limit":{"context":1048576,"output":131072}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	spec := "@magpie-community/opencode-cline-auth"
	write := func(name string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		os.MkdirAll(settings.Dir(), 0o700)
		if err := os.WriteFile(filepath.Join(settings.Dir(), name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("plugins.json", map[string]any{"plugins": []map[string]any{{"spec": spec}}})
	write("plugin-auth.json", map[string]map[string]any{"cline": {"type": "api", "key": "sk-cline-1234"}})
	plugin.UseCached([]plugin.Provider{{ID: "cline", Spec: spec, Name: "Cline",
		Methods:  []plugin.Method{{Type: "api", Label: "API key"}},
		Accounts: []plugin.Account{{Key: "cline", Type: "api"}},
		Models: []plugin.Model{
			{ID: "cline-pass/glm-5.3-flash", Name: "GLM 5.3 Flash (ClinePass)"},
			{ID: "cline-free/mimo-v2.6-flash", Name: "MiMo V2.6 Flash (free)", Free: true},
			{ID: "stealth/space-bunny-alpha", Name: "Space Bunny Alpha", Context: 1_000_000},
		}}})
	t.Cleanup(func() { plugin.UseCached(nil) })
}

func pluginWindow(t *testing.T, id string) int {
	t.Helper()
	for _, e := range Served() {
		if e.ID == id {
			return e.Context
		}
	}
	t.Fatalf("no %s in %v", id, IDs())
	return 0
}

// A plugin's model listed without a window (Cline's feed says none) takes
// the one models.dev gives its model under the vendor's prefix, and the
// window the user sets on the provider over both (ARNO on Discord, v0.1.815).
func TestPluginModelWindow(t *testing.T) {
	clinePlugin(t)
	p, err := Find("cline")
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int{"cline/cline-pass/glm-5.3-flash": 1_000_000, "cline/cline-free/mimo-v2.6-flash": 1_048_576, "cline/stealth/space-bunny-alpha": 1_000_000} {
		if got := pluginWindow(t, id); got != want {
			t.Errorf("%s: %d, want %d", id, got, want)
		}
	}
	if err := SetContext(*p, "cline-free/mimo-v2.6-flash", 400_000); err != nil {
		t.Fatal(err)
	}
	if got := pluginWindow(t, "cline/cline-free/mimo-v2.6-flash"); got != 400_000 {
		t.Errorf("set: %d", got)
	}
	// the page lists it so too (gui's providerInfo reads WindowOf)
	p, _ = Find("cline")
	for _, m := range p.Available() {
		if m.ID == "cline-free/mimo-v2.6-flash" && (p.WindowOf(m) != 400_000 || ListedWindow(m) != 1_048_576) {
			t.Errorf("WindowOf %d, ListedWindow %d", p.WindowOf(m), ListedWindow(m))
		}
	}
}

// A plugin model naming both a window and a prompt cap keeps the cap, but
// a cap above the window is held to the window: opencode/hy3-free's row
// (190K window, 192K input) was served past its window, and GPT-5's real
// 272K prompt cap still wins over its 400K window (#1286).
func TestPluginCatalogClampsInputAboveWindow(t *testing.T) {
	pp := plugin.Provider{ID: "opencode", Models: []plugin.Model{
		{ID: "hy3-free", Context: 190_000, Input: 192_000, Output: 64_000},
		{ID: "gpt-5-codex", Context: 400_000, Input: 272_000, Output: 128_000},
		{ID: "input-only", Input: 200_000, Output: 64_000},
		{ID: "context-only", Context: 1_000_000, Output: 64_000},
	}}
	got := map[string]int{}
	for _, m := range pluginCatalog(pp) {
		got[m.ID] = m.Context
	}
	want := map[string]int{
		"hy3-free":     190_000,
		"gpt-5-codex":  272_000,
		"input-only":   200_000,
		"context-only": 1_000_000,
	}
	for id, n := range want {
		if got[id] != n {
			t.Errorf("%s: %d, want %d", id, got[id], n)
		}
	}
}
