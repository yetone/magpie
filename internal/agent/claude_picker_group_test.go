package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// yanjieee (#1575): with one subscription's 30 models and a routing group,
// saving the group set Claude Code's model and every tier to group/<id>,
// while /model's modelPicker held only the subscription's 30 models, so
// the model Claude Code was on wasn't in /model. Every routing group is in
// the picker, first, as Claude Code is given it; a group saved later is
// there after the next Sync.
func TestClaudePickerListsRoutingGroups(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	models := []string{"claude-haiku-5-5", "deepseek-v4.1-flash"}
	for i := len(models); i < 30; i++ {
		models = append(models, fmt.Sprintf("model-%02d", i))
	}
	if err := provider.Save(provider.Provider{ID: "commandcode", Name: "Command Code", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: models}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "auto-deepseek-v4-1-flash", Name: "Auto DeepSeek", Members: []string{"commandcode/deepseek-v4.1-flash", "commandcode/model-05"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"theme":"dark"}`)
	a := claude(home)
	picker := func() []map[string]string {
		t.Helper()
		var p struct{ Options []map[string]string }
		raw, _ := edit.GetJSON(path, "modelPicker")
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatalf("modelPicker: %v\n%s", err, readFile(path))
		}
		return p.Options
	}
	listed := func() []string {
		var out []string
		for _, o := range picker() {
			out = append(out, o["model"])
		}
		return out
	}

	if err := a.Apply("model", "group/auto-deepseek-v4-1-flash"); err != nil {
		t.Fatal(err)
	}
	main := writtenModel(path)
	if !strings.HasPrefix(main, "auto/deepseek-v4.1-flash") {
		t.Fatalf("model: %q\n%s", main, readFile(path))
	}
	opts := picker()
	if len(opts) != 31 {
		t.Fatalf("picker has %d options, want the group and 30 models: %v", len(opts), listed())
	}
	// the group, first, as the model Claude Code was set to is spelled
	if g := opts[0]; g["model"] != main || g["label"] != "Auto DeepSeek · routing group" || g["description"] != "via magpie" {
		t.Fatalf("first option %v, want the group as %q: %v", g, main, listed())
	}
	for _, m := range models {
		if !slices.ContainsFunc(listed(), func(v string) bool {
			return strings.TrimSuffix(v, "[1m]") == "commandcode/"+provider.ClaudeDashed(m) || strings.TrimSuffix(v, "[1m]") == "commandcode/"+m
		}) {
			t.Fatalf("picker has no commandcode/%s: %v", m, listed())
		}
	}

	// a group saved after it is in /model once magpie syncs Claude Code
	if err := provider.SaveGroup(provider.Group{ID: "second", Name: "Second", Members: []string{"commandcode/model-07"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(listed(), func(v string) bool { return strings.TrimSuffix(v, "[1m]") == "group/second" }) {
		t.Fatalf("a group saved later isn't in the picker: %v", listed())
	}
}
