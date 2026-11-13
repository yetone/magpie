package agent

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// ccdcn on X: in the Codex app, the models of a provider the user added
// (XJAI's model-code, model-work, model-test), which nothing magpie reads
// knows, had no reasoning effort to pick. magpie-models.json, which Codex
// reads them from, now offers them low, medium and high, as GET
// /v1/codex/models does (provider.CodexEfforts).
func TestCodexCatalogOffersUnknownModelsLevels(t *testing.T) {
	home := syncHome(t)
	if err := provider.Save(provider.Provider{ID: "xjai", Name: "XJAI", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"model-code", "model-work", "model-test"}}); err != nil {
		t.Fatal(err)
	}
	cx := codex(home)
	if err := cx.Fields[0].Set("xjai/model-test"); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Models []struct {
			Slug    string  `json:"slug"`
			Default *string `json:"default_reasoning_level"`
			Levels  []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	raw := readFile(filepath.Join(home, ".codex", "magpie-models.json"))
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	seen := 0
	for _, m := range got.Models {
		if m.Slug != "xjai/model-code" && m.Slug != "xjai/model-work" && m.Slug != "xjai/model-test" {
			continue
		}
		seen++
		var ls []string
		for _, l := range m.Levels {
			ls = append(ls, l.Effort)
		}
		// and Ultra, which Codex sends as high, the top of them (pingqipai3 on X)
		if !slices.Equal(ls, []string{"low", "medium", "high", "ultra"}) || m.Default == nil {
			t.Errorf("%s offers %v (default %v), want low, medium, high, ultra", m.Slug, ls, m.Default)
		}
	}
	if seen != 3 {
		t.Errorf("%d of XJAI's models in magpie-models.json: %s", seen, raw)
	}
	// and magpie's own effort control for Codex offers the same
	if opts := codexEfforts(magpieModels("codex"), "xjai/model-test"); len(opts) != 4 || opts[0].Value != "" {
		t.Errorf("effort control: %+v", opts)
	}
}
