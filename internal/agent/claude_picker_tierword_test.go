package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// LBaoheng24070 on X: Sonnet put on a DeepSeek model in magpie, with
// Claude Code's own models hidden, and an app that runs Claude Code with
// "sonnet" (AWS Quick, through claude-agent-acp) matched the word against
// /model's rows by name. replaceBuiltInOptions had taken the lineup's
// tier rows out, so no row was "sonnet" and the match fell to the real
// "Claude Sonnet" row: 58 requests went to Claude. With the lineup
// replaced, each tier a model is on has a row whose model is the tier's
// word, named after that model, and the model's own row goes, as beside
// the lineup; a Claude model on no tier keeps its row for a user who
// picks it.
func TestClaudePickerKeepsTierWordsWithLineupReplaced(t *testing.T) {
	home := syncHome(t)
	for _, p := range []provider.Provider{
		{ID: "ds", Name: "DeepSeek", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-v4", "deepseek-v4-flash"}},
		{ID: "an", Name: "Anthropic", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"claude-sonnet-5-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetOwnModelsHidden("claude", true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{}`)
	a := claude(home)
	for _, kv := range [][2]string{{"model", "ds/deepseek-v4"}, {"sonnet", "ds/deepseek-v4"}, {"haiku", "ds/deepseek-v4-flash"}} {
		if err := a.Field(kv[0]).Set(kv[1]); err != nil {
			t.Fatalf("%s=%s: %v", kv[0], kv[1], err)
		}
	}
	raw, _ := edit.GetJSON(path, "modelPicker")
	var p struct {
		Options []map[string]string `json:"options"`
		Replace bool                `json:"replaceBuiltInOptions"`
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("modelPicker: %v\n%s", err, readFile(path))
	}
	if !p.Replace {
		t.Fatalf("own models hidden, no replaceBuiltInOptions\n%s", raw)
	}
	rows := map[string]map[string]string{}
	for _, o := range p.Options {
		rows[o["model"]] = o
	}
	for _, w := range []string{"sonnet", "haiku"} {
		if rows[w] == nil {
			t.Fatalf("no %q row with the lineup replaced: an app matching %q by name takes another row\n%s", w, w, raw)
		}
	}
	if l := rows["sonnet"]["label"]; l == "" || l == "sonnet" {
		t.Errorf("sonnet row labelled %q, want DeepSeek's model's name\n%s", l, raw)
	}
	for _, m := range []string{"ds/deepseek-v4", "ds/deepseek-v4-flash"} {
		if rows[m] != nil {
			t.Errorf("%s, on a tier, listed again beside the tier's row\n%s", m, raw)
		}
	}
	if rows["an/claude-sonnet-5-5"] == nil {
		t.Errorf("the Claude model on no tier lost its row\n%s", raw)
	}
	if !magpiesPicker(path) {
		t.Errorf("the picker no longer reads as magpie's\n%s", raw)
	}
}
