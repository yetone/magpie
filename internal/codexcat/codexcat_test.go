package codexcat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// Codex's own models, routed through magpie, keep what Codex knows of them
// — image input, context window, tools — under magpie's id; a third-party
// model gets the generic entry.
func TestCodexCatalogKeepsOwnEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"models":[
		{"slug":"gpt-5.5","display_name":"GPT-5.5","priority":3,"visibility":"list","input_modalities":["text","image"],
		 "context_window":272000,"upgrade":{"model":"gpt-6"},"availability_nux":{"message":"new"}}]}`), 0o644)

	var got struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(Catalog([]catalog.Model{
		{ID: "deepseek/deepseek-chat", Name: "deepseek-chat · DeepSeek"},
		{ID: "codex/gpt-5.5", Name: "GPT-5.5 · Codex", Efforts: []string{"low", "high"}},
	}), &got); err != nil || len(got.Models) != 2 {
		t.Fatalf("%v %v", err, got)
	}
	third, own := got.Models[0], got.Models[1]
	if third["slug"] != "deepseek/deepseek-chat" || third["base_instructions"] != Prompt || len(third["input_modalities"].([]any)) != 1 {
		t.Errorf("third-party entry: %v", third)
	}
	if own["slug"] != "codex/gpt-5.5" || own["display_name"] != "GPT-5.5 · Codex" || own["priority"] != float64(2) ||
		own["context_window"] != float64(272000) || len(own["input_modalities"].([]any)) != 2 {
		t.Errorf("own entry: %v", own)
	}
	if own["base_instructions"] != Prompt {
		t.Error("own entry without base_instructions")
	}
	if _, ok := own["upgrade"]; ok {
		t.Error("upgrade prompt kept")
	}
	if _, ok := own["availability_nux"]; ok {
		t.Error("start-up notice kept")
	}
}

// A model that takes images says so, and Codex lets images be attached.
func TestCodexCatalogImages(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	var got struct {
		Models []struct {
			Modalities []string `json:"input_modalities"`
		} `json:"models"`
	}
	json.Unmarshal(Catalog([]catalog.Model{
		{ID: "a/text", Name: "text"},
		{ID: "a/vision", Name: "vision", Images: true},
	}), &got)
	if len(got.Models) != 2 || len(got.Models[0].Modalities) != 1 || len(got.Models[1].Modalities) != 2 || got.Models[1].Modalities[1] != "image" {
		t.Errorf("%+v", got.Models)
	}
}

// A model magpie describes has Codex search its MCP tools rather than send
// every one in each request (#258), with its prompt, context window, and
// neither code mode nor Responses Lite. A window above the working window
// is told as that, the whole one as its max, unless settings.FullContext.
func TestCodexCatalogToolSearch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(os.Getenv("HOME"), ".config"))
	var got struct {
		Models []map[string]any `json:"models"`
	}
	json.Unmarshal(Catalog([]catalog.Model{
		{ID: "group/auto-gemini-3-8-flash", Name: "auto", Context: 996147, Efforts: []string{"low", "medium", "high"}, Images: true},
	}), &got)
	if len(got.Models) != 1 {
		t.Fatalf("%v", got)
	}
	e := got.Models[0]
	if e["supports_search_tool"] != true || e["base_instructions"] != Prompt || e["context_window"] != float64(settings.WorkingWindow) || e["max_context_window"] != float64(996147) {
		t.Errorf("entry: %v", e)
	}
	if err := settings.Save(settings.Settings{FullContext: true}); err != nil {
		t.Fatal(err)
	}
	var full struct {
		Models []map[string]any `json:"models"`
	}
	json.Unmarshal(Catalog([]catalog.Model{
		{ID: "group/auto-gemini-3-8-flash", Name: "auto", Context: 996147},
	}), &full)
	if e := full.Models[0]; e["context_window"] != float64(996147) || e["max_context_window"] != nil {
		t.Errorf("full context: %v", e)
	}
	for _, off := range []string{"tool_mode", "use_responses_lite", "multi_agent_version", "model_messages"} {
		if v, ok := e[off]; ok {
			t.Errorf("%s = %v", off, v)
		}
	}
}

// Codex 0.147 refuses a catalog whose entries don't say whether a model
// takes parallel tool calls (#298), and later Codex ask for them anyway.
func TestCodexCatalogParallelToolCalls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	var got struct {
		Models []map[string]any `json:"models"`
	}
	json.Unmarshal(Catalog([]catalog.Model{{ID: "fake/m1", Name: "m1"}, {ID: "group/auto", Name: "auto"}}), &got)
	if len(got.Models) != 2 {
		t.Fatalf("%v", got)
	}
	for _, e := range got.Models {
		if e["supports_parallel_tool_calls"] != true {
			t.Errorf("%v: supports_parallel_tool_calls = %v", e["slug"], e["supports_parallel_tool_calls"])
		}
	}
}

// Fast mode is offered for a ChatGPT account's GPT models, with Codex's own
// tiers when it lists the model, and for no one else's.
func TestCodexCatalogServiceTiers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"models":[
		{"slug":"gpt-6-astra","display_name":"GPT-6 Astra","service_tiers":[{"id":"priority","name":"Fast","description":"2x speed, increased usage"}]}]}`), 0o644)

	var got struct {
		Models []struct {
			Slug  string `json:"slug"`
			Tiers []struct {
				ID          string `json:"id"`
				Description string `json:"description"`
			} `json:"service_tiers"`
		} `json:"models"`
	}
	json.Unmarshal(Catalog([]catalog.Model{
		{ID: "codex/gpt-6-astra", Name: "GPT-6 Astra · Codex"},
		{ID: "codex/gpt-6-sol", Name: "GPT-6 Sol · Codex"},
		{ID: "copilot/gpt-6-sol", Name: "GPT-6 Sol · Copilot"},
		{ID: "openrouter/openai/gpt-6-sol", Name: "GPT-6 Sol · OpenRouter"},
	}), &got)
	if len(got.Models) != 4 {
		t.Fatalf("%+v", got.Models)
	}
	if ts := got.Models[0].Tiers; len(ts) != 1 || ts[0].ID != "priority" || ts[0].Description != "2x speed, increased usage" {
		t.Errorf("own entry tiers: %+v", ts)
	}
	if ts := got.Models[1].Tiers; len(ts) != 1 || ts[0].ID != "priority" {
		t.Errorf("uncached account model tiers: %+v", ts)
	}
	for _, m := range got.Models[2:] {
		if m.Tiers == nil || len(m.Tiers) != 0 {
			t.Errorf("%s offers tiers: %+v", m.Slug, m.Tiers)
		}
	}
}
