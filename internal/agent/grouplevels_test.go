package agent

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A routing group's levels as it names them (#295) are what agents are
// given for it: Codex's catalog, Pi's thinkingLevelMap — not only those
// its members share.
func TestGroupLevelsReachAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"flash", "sol"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("a/flash", []string{"low", "high", "max"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("a/sol", []string{"none", "low", "medium", "high", "xhigh", "max"}); err != nil {
		t.Fatal(err)
	}
	all := []string{"none", "low", "medium", "high", "xhigh", "max"}
	if err := provider.SaveGroup(provider.Group{Name: "mix", Members: []string{"a/sol", "a/flash"}, Levels: all}); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(magpieModels("codex"), func(m catalog.Model) bool { return m.ID == "group/mix" })
	if i < 0 || !slices.Equal(magpieModels("codex")[i].Efforts, all) {
		t.Fatalf("codex: %+v", magpieModels("codex"))
	}
	pi, _ := json.Marshal(magpieProviderJSON("pi"))
	type piModel struct {
		ID     string         `json:"id"`
		Levels map[string]any `json:"thinkingLevelMap"`
	}
	var cfg struct {
		Models []piModel `json:"models"`
	}
	json.Unmarshal(pi, &cfg)
	j := slices.IndexFunc(cfg.Models, func(m piModel) bool { return m.ID == "group/mix" })
	if j < 0 || cfg.Models[j].Levels["medium"] != "medium" || cfg.Models[j].Levels["xhigh"] != "xhigh" {
		t.Fatalf("pi: %s", pi)
	}
}
