package codexcat

import (
	"encoding/json"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A model offering Codex's Ultra that no ChatGPT account answers for
// (Copilot's gpt-6.1-sol, #656) says multi-agent V2, as Codex's own entry
// for the model does: Ultra hands work to Codex's agents in V2 alone. The
// "subagents on any model" setting (V1) still stamps the OpenAI entries
// alone.
func TestUltraEntriesSayV2(t *testing.T) {
	v1Home(t, `{"etag":"W/\"a\"","models":[]}`)
	ms := []catalog.Model{
		{ID: "copilot/gpt-6.1-sol", Name: "GPT-6.1 Sol", Efforts: []string{"low", "high", "max", "ultra"}, AgentsV2: true},
		{ID: "copilot/gpt-6-luna", Name: "GPT-6 Luna", Efforts: []string{"low", "high", "max"}},
		{ID: "group/sol", Name: "sol", Efforts: []string{"low", "high", "max", "ultra"}, Fast: true},
	}
	for _, on := range []bool{false, true} {
		setV1(t, on)
		got := versions(t, ms)
		want := map[bool]any{false: nil, true: "v1"}[on]
		if got["copilot/gpt-6.1-sol"] != "v2" || got["copilot/gpt-6-luna"] != nil || got["group/sol"] != want {
			t.Fatalf("V1 setting %v: %v", on, got)
		}
	}
}

// #1108: Codex's own entry for gpt-6-astra says multi_agent_reasoning_effort
// xhigh, and its Ultra sends that; s2a/gpt-6-astra's entry had none, so
// Codex sent max for the same model. A third-party entry for a model Codex
// has an entry for now says the same effort; one for a model whose own entry
// says none (gpt-6-sol), one not offering Ultra, and one that doesn't reach
// that effort say nothing, and Codex keeps sending max.
func TestUltraEffortAsCodexOwnEntry(t *testing.T) {
	levels := `[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"xhigh"},{"effort":"max"},{"effort":"ultra"}]`
	v1Home(t, `{"etag":"W/\"a\"","models":[`+
		`{"slug":"gpt-6-astra","multi_agent_version":"v2","multi_agent_reasoning_effort":"xhigh","supported_reasoning_levels":`+levels+`},`+
		`{"slug":"gpt-6-sol","multi_agent_version":"v2","supported_reasoning_levels":`+levels+`}]}`)
	all := []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	ms := []catalog.Model{
		{ID: "s2a/gpt-6-astra", Name: "gpt-6-astra", Efforts: all, AgentsV2: true},
		{ID: "relay/openai/gpt-6-astra-2026-09-14", Name: "astra snapshot", Efforts: all, AgentsV2: true},
		{ID: "s2a/gpt-6-sol", Name: "gpt-6-sol", Efforts: all, AgentsV2: true},
		{ID: "cheap/gpt-6-astra", Name: "no ultra", Efforts: []string{"low", "high", "xhigh"}},
		{ID: "short/gpt-6-astra", Name: "no xhigh", Efforts: []string{"low", "high", "max", "ultra"}, AgentsV2: true},
	}
	var got struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(Catalog(ms), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"s2a/gpt-6-astra": "xhigh", "relay/openai/gpt-6-astra-2026-09-14": "xhigh",
		"s2a/gpt-6-sol": nil, "cheap/gpt-6-astra": nil, "short/gpt-6-astra": nil}
	for _, m := range got.Models {
		slug := m["slug"].(string)
		if m["multi_agent_reasoning_effort"] != want[slug] {
			t.Errorf("%s: multi_agent_reasoning_effort %v, want %v", slug, m["multi_agent_reasoning_effort"], want[slug])
		}
	}
}

// pingqipai3 on X: Codex offered Ultra on no model of another vendor's, so
// a Claude, DeepSeek or GLM lead never handed work to Codex's agents
// unasked. Codex sends a model its own top level for Ultra (max, or the
// highest below it) and is proactive with its agents at Ultra in V2 alone,
// so each model of another vendor's that reasons offers Ultra after its
// levels, at V2, its default still one of its own levels. An OpenAI model
// keeps OpenAI's word (Luna stops at max), a model that doesn't reason and
// one whose lead seals its subagents' tasks (a ChatGPT account's group,
// a relay of the ChatGPT backend) get none.
func TestUltraOnOtherVendorsModels(t *testing.T) {
	v1Home(t, `{"etag":"W/\"a\"","models":[]}`)
	ms := []catalog.Model{
		{ID: "anthropic/claude-opus-4-8", Name: "Claude Opus", Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
		{ID: "zhipu/glm-5.3", Name: "GLM-5.3", Efforts: []string{"low", "high"}},
		{ID: "deepseek/deepseek-v4-pro", Name: "DeepSeek V4 Pro", Efforts: []string{"high", "max"}},
		{ID: "group/mixed", Name: "mixed", Efforts: []string{"low", "medium", "high"}},
		{ID: "copilot/gpt-6-luna", Name: "GPT-6 Luna", Efforts: []string{"low", "high", "max"}},
		{ID: "relay/o4-mini", Name: "o4-mini", Efforts: []string{"low", "high"}},
		{ID: "relay/gpt-6-astra", Name: "sealed", Efforts: []string{"low", "high", "max"}, Seals: true},
		{ID: "group/fast", Name: "fast", Efforts: []string{"low", "high"}, Fast: true},
		{ID: "kimi/kimi-k3", Name: "Kimi K3"},
	}
	var got struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(Catalog(ms), &got); err != nil {
		t.Fatal(err)
	}
	ultra := map[string]bool{"anthropic/claude-opus-4-8": true, "zhipu/glm-5.3": true, "deepseek/deepseek-v4-pro": true, "group/mixed": true}
	for _, m := range got.Models {
		slug := m["slug"].(string)
		var levels []string
		for _, l := range m["supported_reasoning_levels"].([]any) {
			levels = append(levels, l.(map[string]any)["effort"].(string))
		}
		has := len(levels) > 0 && levels[len(levels)-1] == "ultra"
		if has != ultra[slug] {
			t.Errorf("%s: levels %v, Ultra wanted %v", slug, levels, ultra[slug])
		}
		if ultra[slug] && m["multi_agent_version"] != "v2" {
			t.Errorf("%s: multi_agent_version %v, want v2", slug, m["multi_agent_version"])
		}
		if d, _ := m["default_reasoning_level"].(string); d == "ultra" {
			t.Errorf("%s: default level ultra", slug)
		}
	}
}
