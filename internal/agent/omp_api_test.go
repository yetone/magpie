package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"gopkg.in/yaml.v3"
)

// omp asks each model on the API its provider serves it on, as Pi does: a
// Claude over Chat lost its thinking's signatures between tool turns. A
// Messages-only provider's models go on anthropic-messages at the gateway,
// a Responses-only one's on openai-responses, and a Chat provider's and a
// group's stay on the provider's openai-completions, keyless.
func TestOmpModelsAskedOnTheirNativeAPI(t *testing.T) {
	home := syncHome(t)
	was := ompVersion
	ompVersion = func() string { return "16.3.5" }
	t.Cleanup(func() { ompVersion = was })
	for _, p := range []provider.Provider{
		{ID: "resp", Name: "Resp", Key: "k", Responses: "http://127.0.0.1:1/v1", Models: []string{"grok-5", "gpt-5.5"}},
		{ID: "openai", Name: "OpenAI", Key: "k", Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1", Models: []string{"gpt-5.5"}},
		{ID: "anth", Name: "Anth", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-sonnet-5", "claude-sonnet-4-5", "kimi-k3"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	var live []catalog.Model
	for _, id := range []string{"claude-sonnet-5", "claude-sonnet-4-5", "kimi-k3"} {
		live = append(live, catalog.Model{ID: id, Efforts: []string{"low", "high", "max"}})
	}
	if err := catalog.SaveLive("anth", "http://127.0.0.1:1", live); err != nil {
		t.Fatal(err)
	}
	if err := omp(home).Field("model").Set("magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".omp", "agent", "models.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := yaml.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	pm := file["providers"].(map[string]any)["magpie"].(map[string]any)
	if pm["api"] != "openai-completions" || pm["baseUrl"] != gatewayV1() || pm["auth"] != "none" {
		t.Fatalf("provider: %v", pm)
	}
	entries := map[string]map[string]any{}
	for _, raw := range pm["models"].([]any) {
		m := raw.(map[string]any)
		entries[m["id"].(string)] = m
	}
	for id, want := range map[string]any{
		"resp/grok-5":            "openai-responses",
		"relay/glm-4.6":          nil,
		"anth/claude-sonnet-5":   "anthropic-messages",
		"anth/claude-sonnet-4-5": "anthropic-messages",
		"anth/kimi-k3":           "anthropic-messages",
		"group/auto-gpt-5-5":     nil,
	} {
		e, ok := entries[id]
		if !ok {
			t.Errorf("%s not listed: %s", id, b)
		} else if e["api"] != want {
			t.Errorf("%s: api %v, want %v", id, e["api"], want)
		}
	}
	// omp posts to baseUrl + /v1/messages; the Claude that refuses a
	// thinking budget thinks adaptively, the others on a budget alone
	for id, mode := range map[string]string{"anth/claude-sonnet-5": "anthropic-adaptive", "anth/claude-sonnet-4-5": "budget", "anth/kimi-k3": "budget"} {
		e := entries[id]
		if e["baseUrl"] != gateway.URL() {
			t.Errorf("%s: baseUrl %v, want %s", id, e["baseUrl"], gateway.URL())
		}
		// and, for omp 16.3.5, its efforts stop at xhigh, the gateway's way
		// to max: it turns the whole models.yml away over a max
		th, _ := e["thinking"].(map[string]any)
		if th["mode"] != mode || fmt.Sprint(th["efforts"]) != "[low high xhigh]" {
			t.Errorf("%s: thinking %v, want mode %s, efforts [low high xhigh]", id, e["thinking"], mode)
		}
	}
	for _, id := range []string{"resp/grok-5", "relay/glm-4.6"} {
		if _, ok := entries[id]["baseUrl"]; ok {
			t.Errorf("%s keeps the provider's baseUrl: %v", id, entries[id])
		}
	}
}
