package agent

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Alma is told which of magpie's models reason, in each model's
// capabilityOverrides: it works a model's capabilities out from models.dev
// by its id, and finds nothing for magpie's (codex/gpt-5.5), so Alma
// offered no thinking for every Codex model (#730). The levels it is given
// are those of the model's that Alma knows, in its order; a model that
// doesn't reason says nothing of it; an override the user set in Alma is
// kept; and a provider already told is left alone.
func TestAlmaToldReasoning(t *testing.T) {
	syncHome(t)
	if err := provider.Save(provider.Provider{ID: "think", Name: "Think", Chat: "https://example.test/v1", Key: "key",
		Models: []string{"gpt-5.5", "switch-only"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("think", "https://example.test/v1", []catalog.Model{
		{ID: "gpt-5.5", Efforts: []string{"xhigh", "minimal", "low", "medium", "high", "ultra"}, Context: 400000, Output: 128000, Images: true},
		{ID: "switch-only", Reasoning: true},
	}); err != nil {
		t.Fatal(err)
	}
	f := startAlma(t)
	a := alma()
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "magpie/think/gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	var mine map[string]any
	for _, p := range f.providers {
		if p["name"] == "magpie" {
			mine = p
		}
	}
	if mine == nil {
		t.Fatalf("no magpie provider: %v", f.providers)
	}
	caps := func(id string) map[string]any {
		t.Helper()
		av, _ := mine["availableModels"].([]any)
		for _, m := range av {
			if o := m.(map[string]any); o["id"] == id {
				c, _ := o["capabilityOverrides"].(map[string]any)
				return c
			}
		}
		t.Fatalf("%s not in %v", id, av)
		return nil
	}
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	if got := js(caps("think/gpt-5.5")); got != `{"contextWindow":400000,"maxOutputTokens":128000,"reasoning":true,"reasoningLevels":["low","medium","high","xhigh"],"vision":true}` {
		t.Fatalf("gpt-5.5: %s", got)
	}
	if c := caps("think/switch-only"); c["reasoning"] != true || c["reasoningLevels"] != nil {
		t.Fatalf("switch-only: %v", c)
	}
	if c := caps("relay/glm-4.6"); c["reasoning"] != nil || c["reasoningLevels"] != nil || js(c["contextWindow"]) != "204800" {
		t.Fatalf("glm-4.6: %v", c)
	}
	f.takeWrites()

	// told already: nothing written
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if w := f.takeWrites(); len(w) != 0 {
		t.Fatalf("sync wrote %v", w)
	}

	// as an older magpie left it, with an override of the user's: told
	// again, the user's kept
	for _, m := range mine["availableModels"].([]any) {
		o := m.(map[string]any)
		switch o["id"] {
		case "think/gpt-5.5":
			o["capabilityOverrides"] = map[string]any{"functionCalling": false}
		case "relay/glm-4.6":
			o["capabilityOverrides"] = map[string]any{"pricing": map[string]any{"input": 1.0}}
		default:
			delete(o, "capabilityOverrides")
		}
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if w := f.takeWrites(); len(w) != 1 || w[0] != "PUT /api/providers/"+mine["id"].(string)+"/models" {
		t.Fatalf("sync wrote %v", w)
	}
	if got := js(caps("think/gpt-5.5")); got != `{"contextWindow":400000,"functionCalling":false,"maxOutputTokens":128000,"reasoning":true,"reasoningLevels":["low","medium","high","xhigh"],"vision":true}` {
		t.Fatalf("gpt-5.5 synced: %s", got)
	}
	if c := caps("think/switch-only"); c["reasoning"] != true || c["reasoningLevels"] != nil {
		t.Fatalf("switch-only synced: %v", c)
	}
	if c := caps("relay/glm-4.6"); c["reasoning"] != nil || js(c["contextWindow"]) != "204800" || js(c["pricing"]) != `{"input":1}` {
		t.Fatalf("glm-4.6 synced: %v", c)
	}
}
