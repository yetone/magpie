package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Nepsama1337 on X: OpenCode was told gpt-6.2-sol's input limit (922000)
// as its context, and no input. models.dev gives the model
// {context:1050000, input:922000, output:128000}, and OpenCode's limit
// takes all three, so all three are written, in OpenCode 1's config and in
// OpenCode 2's. A model models.dev gives one window keeps its context
// alone, and so does a window the user typed, which is no input limit of
// models.dev's.
func TestOpenCodeLimitKeepsContextAndInput(t *testing.T) {
	syncHome(t)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "openai":{"id":"openai","name":"OpenAI","npm":"@ai-sdk/openai","models":{
	    "gpt-6.2-sol":{"id":"gpt-6.2-sol","name":"GPT-6.2 Sol","reasoning":true,"tool_call":true,"release_date":"2026-11-10","modalities":{"input":["text","image"],"output":["text"]},"limit":{"context":1050000,"input":922000,"output":128000},"cost":{"input":2,"output":10}}}},
	  "zai":{"id":"zai","name":"Z.AI","models":{
	    "glm-5.3-flash":{"id":"glm-5.3-flash","name":"GLM-5.3-Flash","tool_call":true,"release_date":"2026-08-26","modalities":{"input":["text"],"output":["text"]},"limit":{"context":1000000,"output":131072}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, p := range []provider.Provider{
		{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-6.2-sol", "glm-5.3-flash"}},
		{ID: "mine", Name: "Mine", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-6.2-sol"}, Contexts: map[string]int{"gpt-6.2-sol": 400000}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{
		"relay/gpt-6.2-sol":   `{"context":1050000,"input":922000,"output":128000}`,
		"relay/glm-5.3-flash": `{"context":1000000,"output":131072}`,
		"mine/gpt-6.2-sol":    `{"context":400000,"output":128000}`,
	}
	limits := func(t *testing.T, raw []byte, at string) {
		t.Helper()
		var got map[string]json.RawMessage
		var top map[string]any
		json.Unmarshal(raw, &top)
		ms, _ := json.Marshal(top[at])
		if err := json.Unmarshal(ms, &got); err != nil {
			t.Fatalf("%v: %s", err, raw)
		}
		for id, w := range want {
			var m struct {
				Limit json.RawMessage `json:"limit"`
			}
			json.Unmarshal(got[id], &m)
			if string(m.Limit) != w {
				t.Errorf("%s: limit %s, want %s", id, m.Limit, w)
			}
		}
	}
	block := magpieProviderJSON("opencode")
	b, _ := json.Marshal(block)
	limits(t, b, "models")
	// OpenCode 2's words for the same block keep the limit as it is
	b2, _ := json.Marshal(openCodeNative(block))
	limits(t, b2, "models")
}
