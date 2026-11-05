package codexcat

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A DeepSeek model's entry says what DeepSeek's own Codex setup says of
// its models (cdn.deepseek.com/api-docs/codex-deepseek-setup-en.sh, its
// models.json; Rollkey8 on X): the shell_command tool, low verbosity, no
// reasoning summary asked for, Codex's agents in V2, an image at its
// original detail on the one that takes images. Its prompt stays magpie's
// (never rewritten), and every other vendor's entry, a routing group and
// a model distilled onto another's base are as they were.
func TestCodexCatalogDeepSeekAsDeepSeekSays(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	var got struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(Catalog([]catalog.Model{
		{ID: "deepseek/deepseek-v4-pro", Name: "DeepSeek V4 Pro", Context: 1048576, Efforts: []string{"low", "high", "max"}},
		{ID: "deepseek/deepseek-flash", Name: "DeepSeek V4.1 Flash", Context: 1048576, Images: true, Efforts: []string{"low", "high", "max"}},
		{ID: "relay/deepseek-ai/DeepSeek-V4-Pro-0813", Name: "DeepSeek V4 Pro 0813", Context: 1000000},
		{ID: "opencode-go/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", Context: 1048576, Images: true},
		{ID: "moonshot/kimi-k3", Name: "Kimi K3", Context: 262144, Images: true, Efforts: []string{"low", "high"}},
		{ID: "zai/glm-5.2", Name: "GLM-5.2", Context: 200000},
		{ID: "group/deepseek", Name: "deepseek"},
		{ID: "x/deepseek-r1-distill-qwen-32b", Name: "R1 Distill Qwen"},
	}), &got); err != nil || len(got.Models) != 8 {
		t.Fatalf("%v %v", err, got)
	}
	by := map[string]map[string]any{}
	for _, e := range got.Models {
		by[e["slug"].(string)] = e
	}
	for _, slug := range []string{"deepseek/deepseek-v4-pro", "deepseek/deepseek-flash", "relay/deepseek-ai/DeepSeek-V4-Pro-0813"} {
		e := by[slug]
		if e["shell_type"] != "shell_command" || e["support_verbosity"] != true || e["default_verbosity"] != "low" ||
			e["default_reasoning_summary"] != "none" || e["multi_agent_version"] != "v2" {
			t.Errorf("%s: shell %v, verbosity %v/%v, summary %v, agents %v; want DeepSeek's shell_command, true/low, none, v2",
				slug, e["shell_type"], e["support_verbosity"], e["default_verbosity"], e["default_reasoning_summary"], e["multi_agent_version"])
		}
		if e["base_instructions"] != Prompt {
			t.Errorf("%s: base_instructions changed; magpie never rewrites a model's Codex prompt", slug)
		}
		if e["truncation_policy"].(map[string]any)["limit"] != float64(10000) || e["apply_patch_tool_type"] != "freeform" {
			t.Errorf("%s: truncation %v, apply_patch %v", slug, e["truncation_policy"], e["apply_patch_tool_type"])
		}
	}
	if by["deepseek/deepseek-flash"]["supports_image_detail_original"] != true {
		t.Error("deepseek-flash takes images: supports_image_detail_original true, as DeepSeek's setup says")
	}
	// A relay's DeepSeek takes images at no detail Codex names: OpenCode
	// Go answers 400 "invalid image detail" to "original" (qiu_eth)
	if e := by["opencode-go/deepseek-v4.1-flash"]; e["supports_image_detail_original"] != nil || e["shell_type"] != "shell_command" {
		t.Errorf("opencode-go/deepseek-v4.1-flash: supports_image_detail_original %v, shell %v; want none, DeepSeek's shell_command",
			e["supports_image_detail_original"], e["shell_type"])
	}
	if in, _ := by["opencode-go/deepseek-v4.1-flash"]["input_modalities"].([]any); len(in) != 2 {
		t.Errorf("opencode-go/deepseek-v4.1-flash input_modalities %v; it takes images", in)
	}
	if v, ok := by["deepseek/deepseek-v4-pro"]["supports_image_detail_original"]; ok {
		t.Errorf("deepseek-v4-pro takes no images; supports_image_detail_original %v", v)
	}
	if in := by["deepseek/deepseek-v4-pro"]["input_modalities"].([]any); len(in) != 1 || in[0] != "text" {
		t.Errorf("deepseek-v4-pro input_modalities %v; DeepSeek lists it text only", in)
	}
	for _, slug := range []string{"moonshot/kimi-k3", "zai/glm-5.2", "group/deepseek", "x/deepseek-r1-distill-qwen-32b"} {
		e := by[slug]
		if e["shell_type"] != "unified_exec" || e["support_verbosity"] != false || e["default_verbosity"] != nil {
			t.Errorf("%s: shell %v, verbosity %v/%v; another vendor's entry changed", slug, e["shell_type"], e["support_verbosity"], e["default_verbosity"])
		}
		for _, k := range []string{"default_reasoning_summary", "multi_agent_version", "supports_image_detail_original"} {
			if v, ok := e[k]; ok {
				t.Errorf("%s: %s %v; another vendor's entry changed", slug, k, v)
			}
		}
	}
}

// default_reasoning_summary is written on DeepSeek's models alone, so a
// value the user put by hand in another model's entry stays, as it did
// before magpie wrote the key anywhere; on a DeepSeek model magpie's is
// the one Codex reads (config.toml's model_reasoning_summary still comes
// before both).
func TestCodexCatalogKeepsUsersSummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	ms := []catalog.Model{
		{ID: "deepseek/deepseek-v4-pro", Name: "DeepSeek V4 Pro"},
		{ID: "zai/glm-5.2", Name: "GLM-5.2"},
	}
	cur := []byte(`{"models":[
		{"slug":"deepseek/deepseek-v4-pro","default_reasoning_summary":"detailed"},
		{"slug":"zai/glm-5.2","default_reasoning_summary":"concise"}]}`)
	var got struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(Keep(cur, Catalog(ms)), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"deepseek/deepseek-v4-pro": "none", "zai/glm-5.2": "concise"}
	for _, e := range got.Models {
		if s := e["slug"].(string); e["default_reasoning_summary"] != want[s] {
			t.Errorf("%s: default_reasoning_summary %v; want %s", s, e["default_reasoning_summary"], want[s])
		}
	}
}
