package gui

import (
	"os"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A provider's models say their maker, so its editor draws them by maker
// (WJaunita45604 on X): known by the id, a relay's vendor/ prefix taken
// off, else by the name (Bedrock's us.anthropic. ids); none for a model of
// a maker magpie doesn't know.
func TestProviderModelsSayTheirMaker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-aaaa1111"}
	if err := catalog.SaveLive("relay", "https://relay.example/v1", []catalog.Model{
		{ID: "gpt-6.1"},
		{ID: "claude-opus-5-5"},
		{ID: "us.anthropic.claude-sonnet-4-5-20250929-v1:0", Name: "Claude Sonnet 4.5"},
		{ID: "deepseek/deepseek-v4.1"},
		{ID: "Qwen3.8-Max"},
		{ID: "glm-plan", Name: "GLM-5.3"},
		{ID: "llama-4-maverick"},
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"gpt-6.1": "openai", "claude-opus-5-5": "claude-color", "us.anthropic.claude-sonnet-4-5-20250929-v1:0": "claude-color",
		"deepseek/deepseek-v4.1": "deepseek-color", "Qwen3.8-Max": "qwen-color", "glm-plan": "zhipu-color", "llama-4-maverick": "",
	}
	got := map[string]string{}
	for _, m := range providerInfo(p, nil).Models {
		got[m.ID] = m.Maker
	}
	for id, maker := range want {
		if g, ok := got[id]; !ok || g != maker {
			t.Errorf("%s: maker %q (listed %v), want %q", id, g, ok, maker)
		}
	}
}

// An agent's model list says each model's maker as a provider's editor
// does (modelMaker), so grouped by maker (elis_pina on X) a model stands
// under the same maker in both: Bedrock's us.anthropic. id by its name
// under Anthropic, not under Other.
func TestAgentModelsSayTheirMaker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ids := []string{"gpt-6.1", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", "glm-plan", "llama-4-maverick"}
	if err := catalog.SaveLive("relay", "https://relay.example/v1", []catalog.Model{
		{ID: ids[0]}, {ID: ids[1], Name: "Claude Sonnet 4.5"}, {ID: ids[2], Name: "GLM-5.3"}, {ID: ids[3]},
	}); err != nil {
		t.Fatal(err)
	}
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-aaaa1111", Models: ids}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	editor := map[string]string{}
	for _, m := range providerInfo(p, nil).Models {
		editor["relay/"+m.ID] = m.Maker
	}
	a, err := agent.Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range agentModelList(a) {
		got[m.ID] = m.Maker
	}
	want := map[string]string{"relay/gpt-6.1": "openai", "relay/" + ids[1]: "claude-color", "relay/glm-plan": "zhipu-color", "relay/llama-4-maverick": ""}
	for id, maker := range want {
		if g, ok := got[id]; !ok || g != maker || editor[id] != maker {
			t.Errorf("%s: the list's maker %q (listed %v), the editor's %q, want %q", id, g, ok, editor[id], maker)
		}
	}
}
