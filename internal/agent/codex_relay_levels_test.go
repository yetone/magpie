package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// maomao_00001 on Discord: in the Codex app, a relay's GPT model offered
// low, medium and high alone, none of the levels above. A relay names a
// known model as OpenAI's own list does, by a dated snapshot
// (gpt-6.1-sol-2026-09-29), or renames it (codex-gpt-6.1-sol, a tag in
// brackets, one the user said is gpt-6.1-sol in magpie): each is offered
// that model's levels in magpie-models.json, Ultra on one Codex offers it
// on. A model nothing knows keeps low, medium and high, and another
// vendor's model and a model that stops at max keep their own.
func TestCodexCatalogRelaysKnownModelsLevels(t *testing.T) {
	home := syncHome(t)
	// models.dev's rows, as it listed them on 2026-11-07
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "openai":{"id":"openai","name":"OpenAI","npm":"@ai-sdk/openai","models":{
	    "gpt-6.1-sol":{"id":"gpt-6.1-sol","name":"GPT-6.1 Sol","description":"Near-Astra performance for complex coding, computer use, and professional work at a lower cost.","family":"gpt-sol","attachment":true,"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high","xhigh","max"]}],"tool_call":true,"structured_output":true,"temperature":false,"knowledge":"2026-04-30","release_date":"2026-09-29","last_updated":"2026-09-29","modalities":{"input":["text","image","pdf"],"output":["text"]},"open_weights":false,"limit":{"context":1050000,"input":922000,"output":128000},"experimental":{"modes":{"fast":{"cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5},"provider":{"body":{"service_tier":"priority"}}},"pro":{"provider":{"body":{"reasoning":{"mode":"pro"}}}},"ultrafast":{"cost":{"input":12,"output":60,"cache_read":0.6,"cache_write":15},"provider":{"body":{"service_tier":"ultrafast"}}}}},"cost":{"input":2,"output":10,"cache_read":0.1,"cache_write":2.5,"tiers":[{"input":4,"output":15,"cache_read":0.2,"cache_write":5,"tier":{"type":"context","size":272000}}],"context_over_200k":{"input":4,"output":15,"cache_read":0.2,"cache_write":5}},"canonical_model_id":"openai/gpt-6.1-sol"},
	    "gpt-6-luna":{"id":"gpt-6-luna","name":"GPT-6 Luna","description":"OpenAI's most efficient model for focused, high-volume tasks","family":"gpt-luna","attachment":true,"reasoning":true,"reasoning_options":[{"type":"effort","values":["none","low","medium","high","xhigh","max"]}],"tool_call":true,"structured_output":true,"temperature":false,"knowledge":"2026-05-18","release_date":"2026-09-22","last_updated":"2026-09-22","modalities":{"input":["text","image","pdf"],"output":["text"]},"open_weights":false,"limit":{"context":1050000,"input":922000,"output":128000},"experimental":{"modes":{"fast":{"cost":{"input":0.2,"output":1,"cache_read":0.02,"cache_write":0.25},"provider":{"body":{"service_tier":"priority"}}},"pro":{"provider":{"body":{"reasoning":{"mode":"pro"}}}}}},"cost":{"input":0.1,"output":0.5,"cache_read":0.01,"cache_write":0.125,"tiers":[{"input":0.2,"output":0.75,"cache_read":0.02,"cache_write":0.25,"tier":{"type":"context","size":272000}}],"context_over_200k":{"input":0.2,"output":0.75,"cache_read":0.02,"cache_write":0.25}},"canonical_model_id":"openai/gpt-6-luna"}}},
	  "zai":{"id":"zai","name":"Z.AI","models":{"glm-5.3-flash":{"id":"glm-5.3-flash","name":"GLM-5.3-Flash","description":"Native multimodal GLM model for efficient coding and long-horizon agent tasks","family":"glm-flash","attachment":true,"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high","max"]}],"tool_call":true,"interleaved":{"field":"reasoning_content"},"structured_output":true,"temperature":true,"release_date":"2026-08-26","last_updated":"2026-08-26","modalities":{"input":["text","image","video","pdf"],"output":["text"]},"open_weights":true,"limit":{"context":1000000,"output":131072},"cost":{"input":0.15,"output":0.5,"cache_read":0.03,"cache_write":0},"canonical_model_id":"zhipuai/glm-5.3-flash"}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	s := settings.Load()
	s.ModelSameAs = map[string]string{"relay/sol-of-mine": "gpt-6.1-sol"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	ids := []string{"gpt-6.1-sol", "gpt-6.1-sol-2026-09-29", "codex-gpt-6.1-sol", "[A] gpt-6.1-sol", "sol-of-mine",
		"gpt-6-luna-2026-09-22", "glm-5.3-flash", "model-x-2026-01-01"}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: ids}); err != nil {
		t.Fatal(err)
	}
	if err := codex(home).Fields[0].Set("relay/gpt-6.1-sol-2026-09-29"); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Models []struct {
			Slug   string `json:"slug"`
			Levels []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	raw := readFile(filepath.Join(home, ".codex", "magpie-models.json"))
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	sol := []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	want := map[string][]string{
		"relay/gpt-6.1-sol":            sol,
		"relay/gpt-6.1-sol-2026-09-29": sol,
		"relay/codex-gpt-6.1-sol":      sol,
		"relay/[A] gpt-6.1-sol":        sol,
		"relay/sol-of-mine":            sol,
		// Codex's own catalog stops Luna at max
		"relay/gpt-6-luna-2026-09-22": {"none", "low", "medium", "high", "xhigh", "max"},
		"relay/glm-5.3-flash":         {"low", "high", "max"},
		"relay/model-x-2026-01-01":    {"low", "medium", "high"},
	}
	seen := map[string]bool{}
	for _, m := range got.Models {
		w, ok := want[m.Slug]
		if !ok {
			continue
		}
		seen[m.Slug] = true
		var ls []string
		for _, l := range m.Levels {
			ls = append(ls, l.Effort)
		}
		if !slices.Equal(ls, w) {
			t.Errorf("%s offers %v, want %v", m.Slug, ls, w)
		}
	}
	if len(seen) != len(want) {
		t.Errorf("%d of %d of the relay's models in magpie-models.json: %s", len(seen), len(want), raw)
	}
}
