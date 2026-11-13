package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexcat"
)

// ccdcn on X: the Codex app's picker offered no reasoning effort for the
// models of a provider the user added (model-code, model-work and
// model-test of their XJAI), which nothing magpie reads knows. Codex sent
// them an effort anyway (its chip said 中) and the gateway passed it on,
// but with supported_reasoning_levels [] it couldn't be changed. A model
// nothing says anything about is offered low, medium and high; one known
// to have no levels, one the user said takes no reasoning ask and a
// routing group keep what they had.
func TestCodexOffersLevelsForUnknownModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	os.MkdirAll(filepath.Join(cache, "magpie"), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{
	  "deepseek": {"models": {"deepseek-v4-pro": {"id":"deepseek-v4-pro","name":"DeepSeek V4 Pro","reasoning":true,
	    "reasoning_options":[{"type":"toggle"},{"type":"effort","values":["low","high","max"]}],"limit":{"context":1000000}}}},
	  "plaincat": {"models": {"plain-chat": {"id":"plain-chat","limit":{"context":128000}}}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := Save(Provider{ID: "xjai", Name: "XJAI", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Models: []string{"model-code", "model-work", "model-test", "model-off", "plain-chat", "deepseek-v4-pro"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetModelReasonless("xjai/model-off", true); err != nil {
		t.Fatal(err)
	}
	shown, _ := CatalogFor("codex")
	shown = append(shown, Entry{ID: "group/mix", Model: "group/mix", Name: "mix", Group: "mix", Provider: &Provider{ID: "xjai"}})
	levels := map[string][]string{}
	for _, m := range CodexCatalog(shown) {
		levels[m.ID] = m.Efforts
	}
	want := map[string][]string{
		"xjai/model-code":      {"low", "medium", "high"},
		"xjai/model-work":      {"low", "medium", "high"},
		"xjai/model-test":      {"low", "medium", "high"},
		"xjai/model-off":       nil,
		"xjai/plain-chat":      nil,
		"xjai/deepseek-v4-pro": {"low", "high", "max"},
		"group/mix":            nil,
	}
	for id, w := range want {
		got, ok := levels[id]
		if !ok {
			t.Errorf("%s not in Codex's list: %v", id, levels)
			continue
		}
		if !slices.Equal(got, w) {
			t.Errorf("%s offered %v, want %v", id, got, w)
		}
	}

	// and so the entry Codex reads: levels to pick from, a default among
	// them, and the effort sent (supports_reasoning_summaries, which
	// Codex before 0.145 sends it by)
	var entries []struct {
		Slug    string  `json:"slug"`
		Default *string `json:"default_reasoning_level"`
		Levels  []struct {
			Effort string `json:"effort"`
		} `json:"supported_reasoning_levels"`
		Summaries bool `json:"supports_reasoning_summaries"`
	}
	b, _ := json.Marshal(codexcat.Entries(CodexCatalog(shown), 0))
	if err := json.Unmarshal(b, &entries); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Slug != "xjai/model-test" {
			continue
		}
		if len(e.Levels) != 4 || e.Levels[3].Effort != "ultra" || e.Default == nil || *e.Default != "medium" || !e.Summaries {
			t.Errorf("model-test's entry: %s", b)
		}
		return
	}
	t.Errorf("model-test has no entry: %s", b)
}
