package codexcat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A model of another magpie is handed to Codex in the entry that magpie
// gave its own Codex (provider.codexEntries), under this magpie's id: a
// remote ChatGPT account's GPT model keeps its model_messages, and an
// entry the remote marked " via magpie" says so once.
func TestRemoteMagpieEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(os.Getenv("HOME"), ".config"))
	var got struct {
		Models []map[string]any `json:"models"`
	}
	json.Unmarshal(Catalog([]catalog.Model{
		{ID: "office/codex/gpt-6-astra", Name: "GPT-6-Astra", Efforts: []string{"low", "high"}, Codex: json.RawMessage(`{"slug":"codex/gpt-6-astra","description":"Latest frontier agentic coding model.","base_instructions":"You are Codex","model_messages":{"instructions_template":"T"},"shell_type":"shell_command","supported_reasoning_levels":[{"effort":"low","description":"Fast"},{"effort":"high","description":"Deep"}]}`)},
		{ID: "office/deepseek/deepseek-v4", Name: "DeepSeek V4", Codex: json.RawMessage(`{"slug":"deepseek/deepseek-v4","description":"DeepSeek V4 via magpie","base_instructions":"p","shell_type":"shell_command","supported_reasoning_levels":[]}`)},
	}), &got)
	by := map[string]map[string]any{}
	for _, e := range got.Models {
		slug, _ := e["slug"].(string)
		by[slug] = e
	}
	astra := by["office/codex/gpt-6-astra"]
	if msgs, _ := astra["model_messages"].(map[string]any); msgs["instructions_template"] != "T" {
		t.Errorf("office/codex/gpt-6-astra: %v", astra)
	}
	if d := by["office/deepseek/deepseek-v4"]["description"]; d != "DeepSeek V4 via magpie" {
		t.Errorf("office/deepseek/deepseek-v4's description %q", d)
	}
}
