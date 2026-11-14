package codexcat

import (
	"encoding/json"
	"maps"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// bundledWindows is Codex's own: every model of its bundled models.json
// with the context_window it has there. When Codex is updated, its
// models.json in testdata and this map change together.
func TestBundledWindowsAreCodexOwn(t *testing.T) {
	b, err := os.ReadFile(codexDir + "models.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Models []struct {
			Slug    string `json:"slug"`
			Context int    `json:"context_window"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{}
	for _, m := range file.Models {
		want[m.Slug] = m.Context
	}
	if !maps.Equal(bundledWindows, want) {
		t.Errorf("bundledWindows = %v; Codex's models.json says %v", bundledWindows, want)
	}
	if want["gpt-6.1-terra"] != 500000 {
		t.Errorf("Codex's models.json holds gpt-6.1-terra to %d; this test was written for 500000", want["gpt-6.1-terra"])
	}
}

// @flower__daisy on X: Codex compacted early on magpie. Long
// conversations' default told every model with a longer window to compact
// at 272K, "what OpenAI does with its own models"; Codex's own catalog
// holds GPT-6.1 Terra to 500K. An OpenAI model magpie serves is held to
// the window Codex holds it to, never above its own; another vendor's
// model, and a size the user set, are as before.
func TestCodexCatalogHoldsOpenAIModelToCodexWindow(t *testing.T) {
	models := []catalog.Model{
		{ID: "mycpa/gpt-6.1-terra", Name: "terra", Context: 922000},
		{ID: "auto/gpt-6.1-terra-2026-10-01", Name: "terra dated", Context: 922000},
		{ID: "mycpa/gpt-6.1-sol", Name: "sol", Context: 922000},
		{ID: "small/gpt-6.1-terra", Name: "terra small", Context: 400000},
		{ID: "codex/gpt-6.1-terra", Name: "terra account", Context: 500000},
		{ID: "relay/gpt-6.1-terra", Name: "terra unknown"},
		{ID: "relay/gpt-6.1-sol", Name: "sol unknown"},
		{ID: "deepseek/deepseek-v4-pro", Name: "ds", Context: 1000000},
		{ID: "anthropic/claude-sonnet-5-5", Name: "sonnet", Context: 1000000},
		{ID: "group/terra", Name: "group", Context: 922000},
	}
	told := func() map[string][2]any {
		t.Helper()
		var got struct {
			Models []map[string]any `json:"models"`
		}
		if err := json.Unmarshal(Catalog(models), &got); err != nil {
			t.Fatal(err)
		}
		out := map[string][2]any{}
		for _, e := range got.Models {
			out[e["slug"].(string)] = [2]any{e["context_window"], e["auto_compact_token_limit"]}
		}
		return out
	}
	check := func(what string, want map[string][2]any) {
		t.Helper()
		got := told()
		for id, w := range want {
			if got[id] != w {
				t.Errorf("%s: %s told %v; want %v", what, id, got[id], w)
			}
		}
	}
	work := float64(settings.WorkingWindow)
	// no ChatGPT sign-in: Codex's bundled catalog
	v1Home(t, `{"models":[]}`)
	check("bundled", map[string][2]any{
		"mycpa/gpt-6.1-terra":           {float64(922000), float64(500000)},
		"auto/gpt-6.1-terra-2026-10-01": {float64(922000), float64(500000)},
		"mycpa/gpt-6.1-sol":             {float64(922000), work},
		"small/gpt-6.1-terra":           {float64(400000), nil},
		"codex/gpt-6.1-terra":           {float64(500000), nil},
		"relay/gpt-6.1-terra":           {nil, float64(500000)},
		"relay/gpt-6.1-sol":             {nil, work},
		"deepseek/deepseek-v4-pro":      {float64(1000000), work},
		"anthropic/claude-sonnet-5-5":   {float64(1000000), work},
		"group/terra":                   {float64(922000), work},
	})
	// the ChatGPT account's own list, which is this Codex's, comes first
	v1Home(t, `{"models":[{"slug":"gpt-6.1-terra","display_name":"GPT-6.1-Terra","visibility":"list","context_window":400000,"max_context_window":872000}]}`)
	check("account's list", map[string][2]any{
		"mycpa/gpt-6.1-terra": {float64(922000), float64(400000)},
		"mycpa/gpt-6.1-sol":   {float64(922000), work},
	})
	// a size the user set is theirs, under the window Codex gives too
	v1Home(t, `{"models":[]}`)
	if err := settings.Save(settings.Settings{CompactAt: 300000}); err != nil {
		t.Fatal(err)
	}
	check("compact at 300K", map[string][2]any{
		"mycpa/gpt-6.1-terra": {float64(922000), float64(300000)},
		"relay/gpt-6.1-terra": {nil, float64(300000)},
	})
	if err := settings.Save(settings.Settings{FullContext: true}); err != nil {
		t.Fatal(err)
	}
	check("full window", map[string][2]any{
		"mycpa/gpt-6.1-terra": {float64(922000), nil},
		"relay/gpt-6.1-terra": {nil, nil},
	})
}
