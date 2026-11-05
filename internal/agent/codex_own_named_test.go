package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// nb5p on Discord: Codex's picker drew each of Codex's own models by its
// slug (gpt-6-astra · via magpie · GPT-6-Astra), the only list in magpie
// that did; every other one is drawn by the model's name. Each is drawn
// by its name now, and picked as the slug Codex is set to.
func TestCodexOwnModelsNamedInPicker(t *testing.T) {
	cache := []byte(`{"models":[
		{"slug":"gpt-6-astra","display_name":"GPT-6-Astra","priority":1},
		{"slug":"gpt-6.1-terra","display_name":"GPT-6.1 Terra","priority":2}]}`)
	want := map[string]string{"gpt-6-astra": "GPT-6-Astra", "gpt-6.1-terra": "GPT-6.1 Terra"}
	for _, c := range []struct{ name, auth string }{
		{"beside the ChatGPT sign-in", `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`},
		{"magpie the provider", `{"OPENAI_API_KEY":"sk-x"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, _ := codexHome(t, c.auth, "")
			os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), cache, 0o644)
			if err := codex(home).Fields[0].Set("fake/m1"); err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for _, o := range codex(home).Fields[0].Options(nil) {
				if o.Group == "OpenAI" {
					got[o.Value] = o.Label
				}
			}
			for slug, name := range want {
				if l, ok := got[slug]; !ok || l != name {
					t.Errorf("%s is offered as %q (in the list: %v), want it named %q: %v", slug, l, ok, name, got)
				}
			}
		})
	}
}
