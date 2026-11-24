package agent

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// iTanX (#1616): Codex's Sol as the main model (Opus, Sonnet), Astra on
// Fable and Luna on Haiku at Max. Claude Code's /model leaves out a
// modelPicker row naming a tier's model, comparing ids whole, so Sol's and
// Astra's rows went and Luna's stayed beside "codex/gpt-6-luna:max": a
// model listed twice. A model a tier is on, at whatever effort, has no row
// of its own; one no tier is on keeps its row, and gets it back when the
// tier leaves it.
func TestClaudePickerLeavesOutATiersModelAtItsEffort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	models := []string{"gpt-6.1-sol", "gpt-6-astra", "gpt-6-luna", "gpt-6-terra"}
	if err := provider.Save(provider.Provider{ID: "cx", Name: "Codex", Chat: "https://example.test/v1", Key: "k", Models: models}); err != nil {
		t.Fatal(err)
	}
	var live []catalog.Model
	for _, m := range models {
		live = append(live, catalog.Model{ID: m, Context: 272000, Efforts: []string{"low", "medium", "high", "xhigh", "max"}})
	}
	if err := catalog.SaveLive("cx", "https://example.test/v1", live); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{}`)
	a := claude(home)
	set := func(key, v string) {
		t.Helper()
		if err := a.Field(key).Set(v); err != nil {
			t.Fatalf("%s=%s: %v", key, v, err)
		}
	}
	listed := func() []string {
		t.Helper()
		var p struct{ Options []map[string]string }
		raw, _ := edit.GetJSON(path, "modelPicker")
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatalf("modelPicker: %v\n%s", err, readFile(path))
		}
		var out []string
		for _, o := range p.Options {
			out = append(out, strings.TrimSuffix(o["model"], "[1m]"))
		}
		return out
	}

	set("model", "cx/gpt-6.1-sol")
	set("fable", "cx/gpt-6-astra")
	set("haiku", "cx/gpt-6-luna")
	set("haiku_effort", "max")
	if v, _ := edit.GetJSON(path, "env.ANTHROPIC_DEFAULT_HAIKU_MODEL"); strings.TrimSuffix(v, "[1m]") != "cx/gpt-6-luna:max" {
		t.Fatalf("haiku: %q\n%s", v, readFile(path))
	}
	if got := listed(); slices.Contains(got, "cx/gpt-6-luna") || !slices.Contains(got, "cx/gpt-6-terra") {
		t.Fatalf("picker %v: want Luna, on Haiku at max, left out and Terra, on no tier, listed", got)
	}
	// Luna back off Haiku: its row is back
	set("haiku", "cx/gpt-6-terra")
	if got := listed(); !slices.Contains(got, "cx/gpt-6-luna") || slices.Contains(got, "cx/gpt-6-terra") {
		t.Fatalf("picker %v: want Luna listed again and Terra, now on Haiku at max, left out", got)
	}
	// with Claude Code's own models hidden the picker says
	// replaceBuiltInOptions, which takes the tier rows out of /model too
	// (#4548): magpie lists each tier then as a row of the tier's word,
	// so Terra, on Haiku at max, is in /model as "haiku", not twice, and
	// Luna, on no tier, keeps its own row (LBaoheng24070 on X)
	if err := provider.SetOwnModelsHidden("claude", true); err != nil {
		t.Fatal(err)
	}
	set("model", "cx/gpt-6.1-sol")
	if got := listed(); !slices.Contains(got, "haiku") || slices.Contains(got, "cx/gpt-6-terra") || !slices.Contains(got, "cx/gpt-6-luna") {
		t.Fatalf("picker %v with the built-in rows replaced: want a haiku row for Terra, on Haiku at max, and Luna's row", got)
	}
}
