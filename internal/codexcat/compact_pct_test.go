package codexcat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// Codex compacts at the percent of its window the user picked for it
// (moooyo on Discord: compact once 65% of the context is used). Codex has
// no percent of its own, so it is told auto_compact_token_limit: the
// percent of the window Long conversations gives it, the model's whole
// one under Full window, a model's own Compact at, and a relay's own
// entry's window. Another agent's percent is not Codex's, and none picked
// leaves every entry as it was.
func TestCodexCatalogCompactPercent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(os.Getenv("HOME"), ".config"))
	listed := json.RawMessage(`{"slug":"listed","display_name":"Listed","base_instructions":"x","context_window":200000,"supported_reasoning_levels":[]}`)
	check := func(what string, s settings.Settings, want map[string][2]any) {
		t.Helper()
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
		var got struct {
			Models []map[string]any `json:"models"`
		}
		json.Unmarshal(Catalog([]catalog.Model{
			{ID: "x/big", Name: "big", Context: 1000000},
			{ID: "x/small", Name: "small", Context: 128000},
			{ID: "d/flash", Name: "flash", Context: 1000000, Compact: 500000},
			{ID: "relay/listed", Name: "Listed", Codex: listed},
		}), &got)
		have := map[string][2]any{}
		for _, e := range got.Models {
			have[e["slug"].(string)] = [2]any{e["context_window"], e["auto_compact_token_limit"]}
		}
		for id, w := range want {
			if have[id] != w {
				t.Errorf("%s: %s told window/compact %v; want %v", what, id, have[id], w)
			}
		}
	}
	none := map[string][2]any{
		"x/big":        {float64(1000000), float64(settings.WorkingWindow)},
		"x/small":      {float64(128000), nil},
		"d/flash":      {float64(1000000), float64(500000)},
		"relay/listed": {float64(200000), nil},
	}
	check("none picked", settings.Settings{}, none)
	check("claude's percent", settings.Settings{AgentCompactPcts: map[string]int{"claude": 65}}, none)
	check("65% of the working window", settings.Settings{AgentCompactPcts: map[string]int{"codex": 65}}, map[string][2]any{
		"x/big":        {float64(1000000), float64(176800)},
		"x/small":      {float64(128000), float64(83200)},
		"d/flash":      {float64(1000000), float64(325000)},
		"relay/listed": {float64(200000), float64(130000)},
	})
	check("65% under Full window", settings.Settings{FullContext: true, AgentCompactPcts: map[string]int{"codex": 65}}, map[string][2]any{
		"x/big":   {float64(1000000), float64(650000)},
		"x/small": {float64(128000), float64(83200)},
	})
	check("Codex's own Long conversations", settings.Settings{AgentCompacts: map[string]settings.AgentCompact{"codex": {At: 400000}}, AgentCompactPcts: map[string]int{"codex": 50}}, map[string][2]any{
		"x/big": {float64(1000000), float64(200000)},
	})
	check("100% is the window itself", settings.Settings{FullContext: true, AgentCompactPcts: map[string]int{"codex": 100}}, map[string][2]any{
		"x/big": {float64(1000000), nil},
	})
}
