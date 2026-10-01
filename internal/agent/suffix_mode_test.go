package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// The lists magpie writes into Codex's model_catalog_json and Pi's
// models.json name models by the Provider in model names setting, and are
// written again as it changes: on, as by default, a renamed model too has
// its provider's after it (#203); own, the name the user gave is just as
// they wrote it while a vendor's keeps its provider's (#92); off, none has
// it (#335).
func TestSuffixModesReachAgentFiles(t *testing.T) {
	home := syncHome(t)
	os.WriteFile(catalog.CachePath(), []byte(`{"zai":{"models":{"glm-4.6":{"id":"glm-4.6","name":"GLM-4.6"},"glm-5":{"id":"glm-5","name":"GLM-5"}}}}`), 0o644)
	catalog.Reset()
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Catalog: "zai", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"glm-4.6", "glm-5"}}); err != nil {
		t.Fatal(err)
	}
	piModels := filepath.Join(home, ".pi", "agent", "models.json")
	writeFile(t, piModels, `{"providers":{"magpie":{"name":"magpie","models":[]}}}`)
	codexDir := filepath.Join(home, ".codex")
	codexCat := filepath.Join(codexDir, "magpie-models.json")
	writeFile(t, filepath.Join(codexDir, "config.toml"), "model = \"relay/glm-4.6\"\nmodel_provider = \"magpie\"\nmodel_catalog_json = \""+codexCat+"\"\n")
	writeFile(t, codexCat, `{"models":[]}`)
	catalog.Changed = SyncCatalog
	t.Cleanup(func() { catalog.Changed = nil })
	if err := provider.SetModelName("relay/glm-4.6", "GLM 5.3"); err != nil {
		t.Fatal(err)
	}

	names := func() (pi, codex map[string]string) {
		var p struct {
			Providers map[string]struct {
				Models []map[string]any `json:"models"`
			} `json:"providers"`
		}
		if err := json.Unmarshal([]byte(readFile(piModels)), &p); err != nil {
			t.Fatal(err)
		}
		pi = map[string]string{}
		for _, m := range p.Providers["magpie"].Models {
			pi[m["id"].(string)], _ = m["name"].(string)
		}
		var cx struct {
			Models []struct {
				Slug string `json:"slug"`
				Name string `json:"display_name"`
			} `json:"models"`
		}
		if err := json.Unmarshal([]byte(readFile(codexCat)), &cx); err != nil {
			t.Fatal(err)
		}
		codex = map[string]string{}
		for _, m := range cx.Models {
			codex[m.Slug] = m.Name
		}
		return pi, codex
	}
	for _, c := range []struct{ mode, named, own string }{
		{provider.SuffixOn, "GLM 5.3 · Relay", "GLM-5 · Relay"},
		{provider.SuffixOwn, "GLM 5.3", "GLM-5 · Relay"},
		{provider.SuffixOff, "GLM 5.3", "GLM-5"},
		{provider.SuffixOn, "GLM 5.3 · Relay", "GLM-5 · Relay"},
	} {
		if err := provider.SetSuffixMode(c.mode); err != nil {
			t.Fatal(err)
		}
		pi, codex := names()
		for what, got := range map[string]map[string]string{"pi": pi, "codex": codex} {
			if got["relay/glm-4.6"] != c.named || got["relay/glm-5"] != c.own {
				t.Fatalf("%s, %s: %v", c.mode, what, got)
			}
		}
	}
}
