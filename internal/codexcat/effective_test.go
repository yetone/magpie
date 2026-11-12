package codexcat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Every entry of Codex's own catalog says effective_context_window_percent
// (95); magpie's lacked it (codealc on X: Codex 0.166 on a Copilot Gemini
// worked out no limit and never compacted), for a model with no window
// known as for one with, and for a vendor-listed entry that omitted it.
func TestCodexCatalogSaysEffectivePercent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(os.Getenv("HOME"), ".config"))
	var got struct {
		Models []map[string]any `json:"models"`
	}
	json.Unmarshal(Catalog([]catalog.Model{
		{ID: "copilot/gemini-4-carbon", Name: "Gemini 4 Carbon", Context: 936000},
		{ID: "x/nowindow", Name: "none"},
		{ID: "x/listed", Name: "listed", Context: 200000, Codex: json.RawMessage(`{"slug":"listed","base_instructions":"p"}`)},
	}), &got)
	n := 0
	for _, e := range got.Models {
		slug, _ := e["slug"].(string)
		if slug != "copilot/gemini-4-carbon" && slug != "x/nowindow" && slug != "x/listed" {
			continue
		}
		n++
		if e["effective_context_window_percent"] != float64(95) {
			t.Errorf("%s: effective_context_window_percent %v; want 95", slug, e["effective_context_window_percent"])
		}
	}
	if n != 3 {
		t.Fatalf("%d of 3 entries found", n)
	}
}
