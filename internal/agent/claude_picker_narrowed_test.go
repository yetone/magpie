package agent

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// yyhhyyyyyy on Discord: Claude Code's list in magpie narrowed to a routing
// group's models, and /model still listed others. Claude Code puts its
// built-in lineup above a modelPicker's rows unless the picker says
// replaceBuiltInOptions, and the lineup's Fable row (no tier names it while
// the main model is no Fable) named a model the user hadn't picked. A list
// narrowed in magpie, by picks or by models taken out, now says the flag as
// the own-models switch does; one that leaves nothing out doesn't, and a
// custom row of the user's own (ANTHROPIC_CUSTOM_MODEL_OPTION), which the
// flag hides too, keeps the lineup.
func TestClaudePickerNarrowedListReplacesLineup(t *testing.T) {
	home := syncHome(t)
	shell := claudeShellEnv
	claudeShellEnv = func(string) string { return "" }
	t.Cleanup(func() { claudeShellEnv = shell })
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"glm-4.6", "glm-5"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"theme":"dark"}`)

	picker := func() (models []string, replace bool) {
		t.Helper()
		raw, has := edit.GetJSON(path, "modelPicker")
		if !has {
			t.Fatalf("no modelPicker\n%s", readFile(path))
		}
		var p struct {
			Options []struct {
				Model string `json:"model"`
			} `json:"options"`
			Replace *bool `json:"replaceBuiltInOptions"`
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatalf("modelPicker: %v\n%s", err, readFile(path))
		}
		for _, o := range p.Options {
			models = append(models, o.Model)
		}
		return models, p.Replace != nil && *p.Replace
	}
	sync := func() {
		t.Helper()
		if err := claude(home).Apply("model", "relay/glm-4.6"); err != nil {
			t.Fatal(err)
		}
	}

	// every model listed: Claude Code's lineup stays
	sync()
	if ms, replace := picker(); replace || len(ms) != 2 {
		t.Fatalf("nothing left out: rows %v, replaceBuiltInOptions=%v, want both rows and no flag", ms, replace)
	}

	// one taken out: the lineup goes, the row too; the tiers, which
	// follow glm-4.6, are its rows by their words, as the lineup's are
	// (TestClaudePickerKeepsTierWordsWithLineupReplaced)
	if err := provider.SetHiddenModels("claude", []string{"relay/glm-5"}); err != nil {
		t.Fatal(err)
	}
	sync()
	if ms, replace := picker(); !replace || strings.Join(ms, " ") != "opus sonnet haiku" {
		t.Fatalf("glm-5 taken out: rows %v, replaceBuiltInOptions=%v, want [opus sonnet haiku] and the flag", ms, replace)
	}

	// only the picked ones, glm-5 not among them: the same
	if err := provider.SetOnlyPicked("claude", true); err != nil {
		t.Fatal(err)
	}
	sync()
	if ms, replace := picker(); !replace || slices.Contains(ms, "relay/glm-5") {
		t.Fatalf("only picks: rows %v, replaceBuiltInOptions=%v, want glm-5 left out and the flag", ms, replace)
	}

	// a custom row of the user's own keeps the lineup it is shown in
	if err := edit.SetJSON(path, edit.KV{Path: "env." + claudeCustomOptionEnv, Value: "my-model"}); err != nil {
		t.Fatal(err)
	}
	sync()
	if _, replace := picker(); replace {
		t.Fatalf("with the user's %s: replaceBuiltInOptions set, which hides their row\n%s", claudeCustomOptionEnv, readFile(path))
	}
	if v, _ := edit.GetJSON(path, "env."+claudeCustomOptionEnv); v != "my-model" {
		t.Fatalf("the user's %s is %q now", claudeCustomOptionEnv, v)
	}
	// set in the login shell, the same
	if err := edit.DelJSON(path, "env."+claudeCustomOptionEnv); err != nil {
		t.Fatal(err)
	}
	claudeShellEnv = func(k string) string { return map[string]string{claudeCustomOptionEnv: "my-model"}[k] }
	sync()
	if _, replace := picker(); replace {
		t.Fatalf("with the user's %s in the shell: replaceBuiltInOptions set", claudeCustomOptionEnv)
	}
	claudeShellEnv = func(string) string { return "" }

	// every model picked again: the lineup is back
	if err := provider.SetPickedModels("claude", []string{"relay/glm-4.6", "relay/glm-5"}); err != nil {
		t.Fatal(err)
	}
	sync()
	if ms, replace := picker(); replace || len(ms) != 2 {
		t.Fatalf("every model picked: rows %v, replaceBuiltInOptions=%v, want both rows and no flag", ms, replace)
	}
}
