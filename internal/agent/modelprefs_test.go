package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A model the user names, or keeps some reasoning levels of, is named so
// and offered only those in the lists magpie wrote into the agents' files,
// rewritten as soon as it is changed; a reset gives the model's own back.
func TestModelNameAndLevelsReachAgentFiles(t *testing.T) {
	home := syncHome(t)
	os.WriteFile(catalog.CachePath(), []byte(`{"zai":{"models":{"glm-4.6":{"id":"glm-4.6","name":"GLM-4.6","limit":{"context":204800},
		"reasoning_options":[{"type":"effort","values":["low","medium","high"]}]}}}}`), 0o644)
	catalog.Reset()
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Catalog: "zai", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"glm-4.6"}}); err != nil {
		t.Fatal(err)
	}
	piModels := filepath.Join(home, ".pi", "agent", "models.json")
	writeFile(t, piModels, `{"providers":{"magpie":{"name":"magpie","models":[]}}}`)
	codexDir := filepath.Join(home, ".codex")
	codexCat := filepath.Join(codexDir, "magpie-models.json")
	writeFile(t, filepath.Join(codexDir, "config.toml"), "model = \"relay/glm-4.6\"\nmodel_provider = \"magpie\"\nmodel_catalog_json = '"+codexCat+"'\n")
	writeFile(t, codexCat, `{"models":[]}`)
	catalog.Changed = SyncCatalog
	t.Cleanup(func() { catalog.Changed = nil })

	if err := provider.SetModelName("relay/glm-4.6", "GLM"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("relay/glm-4.6", []string{"low", "high"}); err != nil {
		t.Fatal(err)
	}
	var pi struct {
		Providers map[string]struct {
			Models []map[string]any `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(readFile(piModels)), &pi); err != nil {
		t.Fatal(err)
	}
	ms := pi.Providers["magpie"].Models
	// a custom name still carries its provider, the same as GLM-4.6's own
	// name would, so a picker full of renamed models can still be told
	// apart by vendor
	if len(ms) != 1 || ms[0]["id"] != "relay/glm-4.6" || ms[0]["name"] != "GLM · Relay" {
		t.Fatalf("pi models.json: %v", ms)
	}
	var cx struct {
		Models []struct {
			Slug    string `json:"slug"`
			Name    string `json:"display_name"`
			Efforts []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(readFile(codexCat)), &cx); err != nil {
		t.Fatal(err)
	}
	if len(cx.Models) != 1 || cx.Models[0].Slug != "relay/glm-4.6" || cx.Models[0].Name != "GLM · Relay" {
		t.Fatalf("codex catalog: %+v", cx.Models)
	}
	var levels []string
	for _, e := range cx.Models[0].Efforts {
		levels = append(levels, e.Effort)
	}
	if strings.Join(levels, ",") != "low,high" {
		t.Fatalf("codex levels: %v", levels)
	}

	// unless the custom name already says the provider: it isn't repeated
	if err := provider.SetModelName("relay/glm-4.6", "GLM via Relay"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(codexCat); !strings.Contains(got, `"GLM via Relay"`) || strings.Contains(got, `"GLM via Relay · Relay"`) {
		t.Fatalf("codex catalog, provider repeated:\n%s", got)
	}

	if err := provider.SetModelName("relay/glm-4.6", ""); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("relay/glm-4.6", nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(codexCat); !strings.Contains(got, `"GLM-4.6 · Relay"`) || !strings.Contains(got, `"medium"`) {
		t.Fatalf("codex catalog after reset:\n%s", got)
	}
	if got := readFile(piModels); !strings.Contains(got, `"GLM-4.6 · Relay"`) {
		t.Fatalf("pi models.json after reset:\n%s", got)
	}
}
