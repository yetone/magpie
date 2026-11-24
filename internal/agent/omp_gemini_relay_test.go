package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"gopkg.in/yaml.v3"
)

// TJHHHH on Discord: a Gemini model at a relay with Anthropic's and
// Gemini's URLs (Tokenflow) was written into omp's models.yml on
// anthropic-messages, and every turn reached the relay translated to
// Gemini ("Anthropic → Gemini" in Usage). omp and OmO ask it on Gemini's
// own API at the gateway's /v1beta; so does a model of a provider whose
// Base URL was given as the Gemini one. Pi, which takes Gemini's API out of
// its own folder (piAsked), keeps Messages, and a Claude or GLM at the same
// relay stays on Messages for all three.
func TestOmpGeminiModelAtAMessagesRelay(t *testing.T) {
	home := syncHome(t)
	was := ompVersion
	ompVersion = func() string { return "18.14.0" }
	t.Cleanup(func() { ompVersion = was })
	for _, p := range []provider.Provider{
		{ID: "tokenflow", Name: "Tokenflow", Key: "k", Anthropic: "http://127.0.0.1:1", Gemini: "http://127.0.0.1:1/v1beta", Models: []string{"gemini-4-carbon", "claude-sonnet-4-5", "glm-5"}},
		{ID: "asgem", Name: "AsGem", Key: "k", Chat: "http://127.0.0.1:1/v1", Gemini: "http://127.0.0.1:1/v1beta", BaseAPI: "gemini", Models: []string{"gemini-3-flash", "glm-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := omp(home).Field("model").Set("magpie/tokenflow/gemini-4-carbon"); err != nil {
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
	ompAPIs := map[string]map[string]any{}
	for _, raw := range file["providers"].(map[string]any)["magpie"].(map[string]any)["models"].([]any) {
		m := raw.(map[string]any)
		ompAPIs[m["id"].(string)] = m
	}
	if err := omo(home).Field("model").Set("magpie/tokenflow/gemini-4-carbon"); err != nil {
		t.Fatal(err)
	}
	if err := pi(home).Field("model").Set("magpie/tokenflow/gemini-4-carbon"); err != nil {
		t.Fatal(err)
	}
	jsonModels := func(path string) map[string]map[string]any {
		var f map[string]any
		if err := json.Unmarshal([]byte(readFile(path)), &f); err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string]any{}
		for _, raw := range f["providers"].(map[string]any)["magpie"].(map[string]any)["models"].([]any) {
			m := raw.(map[string]any)
			out[m["id"].(string)] = m
		}
		return out
	}
	omoAPIs := jsonModels(filepath.Join(home, ".omo", "agent", "models.json"))
	piAPIs := jsonModels(filepath.Join(home, ".pi", "agent", "models.json"))
	gem := "google-generative-ai"
	for _, c := range []struct {
		agent   string
		entries map[string]map[string]any
		want    map[string]any
	}{
		{"omp", ompAPIs, map[string]any{"tokenflow/gemini-4-carbon": gem, "tokenflow/claude-sonnet-4-5": "anthropic-messages", "tokenflow/glm-5": "anthropic-messages", "asgem/gemini-3-flash": gem, "asgem/glm-5": nil}},
		{"OmO", omoAPIs, map[string]any{"tokenflow/gemini-4-carbon": gem, "tokenflow/claude-sonnet-4-5": "anthropic-messages", "tokenflow/glm-5": "anthropic-messages", "asgem/gemini-3-flash": gem, "asgem/glm-5": nil}},
		{"Pi", piAPIs, map[string]any{"tokenflow/gemini-4-carbon": "anthropic-messages", "tokenflow/claude-sonnet-4-5": "anthropic-messages", "asgem/gemini-3-flash": nil}},
	} {
		for id, want := range c.want {
			e, ok := c.entries[id]
			if !ok {
				t.Errorf("%s: %s not listed", c.agent, id)
				continue
			}
			if e["api"] != want {
				t.Errorf("%s: %s: api %v, want %v", c.agent, id, e["api"], want)
			}
			if want == gem && e["baseUrl"] != gateway.URL()+"/v1beta" {
				t.Errorf("%s: %s baseUrl: %v", c.agent, id, e["baseUrl"])
			}
		}
	}
}
