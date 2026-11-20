package agent

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// A routing group given another id takes the agents on it along, as a
// provider's rename does: Codex, Claude Code (its model and tiers),
// OpenCode and Pi were left on group/<old>, a group magpie no longer had,
// and the user had to pick it again in each (lucis0116 on X).
func TestRenameGroupMovesTheAgentsOnIt(t *testing.T) {
	home := syncHome(t)
	if err := provider.SaveGroup(provider.Group{ID: "smart", Name: "Smart", Members: []string{"relay/glm-4.6"}}); err != nil {
		t.Fatal(err)
	}
	codexCfg := filepath.Join(home, ".codex", "config.toml")
	codexCat := filepath.Join(home, ".codex", "magpie-models.json")
	writeFile(t, codexCfg, "model = \"group/smart\"\nmodel_provider = \"magpie\"\nmodel_catalog_json = '"+codexCat+"'\n")
	writeFile(t, codexCat, `{"models":[]}`)
	piModels := filepath.Join(home, ".pi", "agent", "models.json")
	piSettings := filepath.Join(home, ".pi", "agent", "settings.json")
	writeFile(t, piModels, `{"providers":{"magpie":{"name":"magpie","models":[]}}}`)
	writeFile(t, piSettings, `{"defaultProvider":"magpie","defaultModel":"group/smart"}`)
	oc := filepath.Join(home, ".config", "opencode", "opencode.json")
	writeFile(t, oc, `{"model":"magpie/group/smart"}`)
	setUpInMagpie("pi", "opencode")
	claudeCfg := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, claudeCfg, `{}`)
	cc, err := Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := cc.Apply("model", "group/smart"); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()

	moved, err := RenameGroup("smart", "clever")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Codex", "Claude Code", "OpenCode", "Pi"} {
		if !slices.Contains(moved, name) {
			t.Errorf("%s not said moved: %v", name, moved)
		}
	}
	if v, _ := edit.GetTOMLTop(codexCfg, "model"); v != "group/clever" {
		t.Errorf("codex model = %q", v)
	}
	if v, _ := edit.GetJSON(piSettings, "defaultModel"); v != "group/clever" {
		t.Errorf("pi defaultModel = %v", v)
	}
	if v, _ := edit.GetJSON(oc, "model"); v != "magpie/group/clever" {
		t.Errorf("opencode model = %v", v)
	}
	claude := readFile(claudeCfg)
	if strings.Contains(claude, "group/smart") || !strings.Contains(claude, `"model": "group/clever"`) && !strings.Contains(claude, `"model":"group/clever"`) {
		t.Errorf("claude settings still on the old id:\n%s", claude)
	}
	for _, f := range []string{codexCat, piModels, oc} {
		if s := readFile(f); strings.Contains(s, "group/smart") || !strings.Contains(s, "group/clever") {
			t.Errorf("%s lists the old id:\n%s", f, s)
		}
	}
}

// Codex on a group magpie found, by its auto/<model>, moves with it when
// the group is made the user's under another id: left on the auto ref,
// Codex named a model its list no longer had (@CHENGJIAHEGOXIS on X).
func TestRenameFoundGroupMovesTheAgentsOnIt(t *testing.T) {
	home := syncHome(t)
	for _, id := range []string{"relay", "relay2"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-v4.1-flash"}}); err != nil {
			t.Fatal(err)
		}
	}
	codexCfg := filepath.Join(home, ".codex", "config.toml")
	codexCat := filepath.Join(home, ".codex", "magpie-models.json")
	writeFile(t, codexCfg, "model = \"auto/deepseek-v4.1-flash\"\nmodel_provider = \"magpie\"\nmodel_catalog_json = '"+codexCat+"'\n")
	writeFile(t, codexCat, `{"models":[]}`)
	SyncCatalog()
	moved, err := RenameGroup("auto-deepseek-v4-1-flash", "deepseek-api-agents")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(moved, "Codex") {
		t.Errorf("Codex not said moved: %v", moved)
	}
	if v, _ := edit.GetTOMLTop(codexCfg, "model"); v != "group/deepseek-api-agents" {
		t.Errorf("codex model = %q", v)
	}
}

// A change to the list alone never restarts the Codex app's app-server:
// a provider renamed rewrites the catalog, and the app shows the new name
// at its next launch (TestCodexEffortPickKeepsTheAppRunning says why).
func TestCodexAppRouteIgnoresARename(t *testing.T) {
	home := syncHome(t)
	codexCat := filepath.Join(home, ".codex", "magpie-models.json")
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), "model = \"relay/glm-4.6\"\nmodel_provider = \"magpie\"\nmodel_catalog_json = '"+codexCat+"'\n")
	writeFile(t, codexCat, `{"models":[]}`)
	SyncCatalog()
	before, _, known := codexAppRoute()
	if !known {
		t.Fatal("route not known")
	}
	if !strings.Contains(readFile(codexCat), "GLM-4.6 · Relay") {
		t.Fatalf("catalog:\n%s", readFile(codexCat))
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Renamed", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"glm-4.6"}}); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	if !strings.Contains(readFile(codexCat), "GLM-4.6 · Renamed") {
		t.Fatalf("catalog not rewritten:\n%s", readFile(codexCat))
	}
	if r, _, _ := codexAppRoute(); r != before {
		t.Fatal("a provider renamed changed the Codex app's route: its app-server is ended for a list")
	}
}
