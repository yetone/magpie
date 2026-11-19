package codexcat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// Another magpie's ChatGPT account's model (its entry's slug codex/<slug>)
// is Codex's own, as a ChatGPT account's here is: given this computer's
// Codex's entry for the slug where its cache has one, model_messages and
// all, which the remote, often a server with no Codex, couldn't give.
// A third party's model named like one (relay/gpt-6-astra) is not.
func TestRemoteChatGPTModelIsCodexOwn(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("CODEX_HOME", filepath.Join(h, ".codex"))
	os.MkdirAll(filepath.Join(h, ".codex"), 0o755)
	cache := `{"etag":"e1","models":[{"slug":"gpt-6-astra","display_name":"GPT-6-Astra","description":"Latest frontier agentic coding model.","base_instructions":"You are Codex","model_messages":{"instructions_template":"T"},"shell_type":"shell_command","supported_reasoning_levels":[{"effort":"low","description":"Fast"}],"visibility":"list"}]}`
	if err := os.WriteFile(catalog.CodexModelsCache(), []byte(cache), 0o644); err != nil {
		t.Fatal(err)
	}
	remote := json.RawMessage(`{"slug":"codex/gpt-6-astra","description":"GPT-6-Astra via magpie","base_instructions":"You are Codex","shell_type":"shell_command","supported_reasoning_levels":[{"effort":"low","description":"Fast"}]}`)
	ms := []catalog.Model{
		{ID: "office/codex/gpt-6-astra", Name: "GPT-6-Astra", Efforts: []string{"low"}, Codex: remote},
		{ID: "relay/gpt-6-astra", Name: "GPT-6-Astra", Efforts: []string{"low"}},
	}
	var got struct {
		Models []map[string]any `json:"models"`
	}
	json.Unmarshal(Catalog(ms), &got)
	by := map[string]map[string]any{}
	for _, e := range got.Models {
		slug, _ := e["slug"].(string)
		by[slug] = e
	}
	if msgs, _ := by["office/codex/gpt-6-astra"]["model_messages"].(map[string]any); msgs["instructions_template"] != "T" {
		t.Errorf("office/codex/gpt-6-astra: %v", by["office/codex/gpt-6-astra"])
	}
	if d := by["office/codex/gpt-6-astra"]["description"]; d != "Latest frontier agentic coding model." {
		t.Errorf("office/codex/gpt-6-astra's description %q, want Codex's own", d)
	}
	if d, _ := by["relay/gpt-6-astra"]["description"].(string); !strings.HasSuffix(d, " via magpie") {
		t.Errorf("relay/gpt-6-astra took Codex's own entry: %v", by["relay/gpt-6-astra"])
	}
	own := OwnIn(ms)
	if !own("office/codex/gpt-6-astra") || !own("codex/gpt-6-sol") || own("relay/gpt-6-astra") {
		t.Error("OwnIn")
	}
}
