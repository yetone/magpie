package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
)

// maxleebeta in #feedback: Pi connected to magpie, picking one of Pi's own
// models in the OPENCODE GO section (opencode-go/<model>, Pi signed in to
// OpenCode Go itself) turned 「接入」 off, with nothing said. The pick
// writes defaultProvider opencode-go, so Pi asks OpenCode Go itself and
// magpie isn't in its config any more; what was missing is that the model
// said so. Each of an agent's own models now says who it asks (Direct), as
// Claude Code's and Codex's do, so the picker tags it "direct, not via
// magpie" and asks before a pick takes the agent off magpie. Every agent
// whose own models come from ownOptions shares this, so OpenCode, Crush
// and omp are held to it too; magpie's own models stay unmarked.
func TestOwnModelsSayTheyGoDirect(t *testing.T) {
	home := syncHome(t)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"opencode-go":{"name":"OpenCode Go","models":{
	  "glm-5.1":{"id":"glm-5.1","name":"GLM-5.1"},
	  "kimi-k2.6":{"id":"kimi-k2.6","name":"Kimi K2.6"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	auth := `{"opencode-go": {"type": "api", "key": "test-key"}}`

	piDir := filepath.Join(home, ".pi", "agent")
	writeFile(t, filepath.Join(piDir, "auth.json"), auth)
	writeFile(t, filepath.Join(piDir, "settings.json"), `{"defaultProvider": "magpie", "defaultModel": "deepseek/deepseek-v4.1-flash"}`)
	writeFile(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"), auth)
	cfg := filepath.Join(home, ".config")
	writeFile(t, filepath.Join(cfg, "crush", "crush.json"), `{"providers": {"opencode-go": {"api_key": "test-key"}}}`)
	writeFile(t, filepath.Join(home, ".omp", "agent", "models.yml"), "providers:\n  opencode-go:\n    models:\n      - id: glm-5.1\n        name: GLM-5.1\n")

	for _, a := range []*Agent{pi(home), opencode(home, cfg), crush(home, cfg), omp(home)} {
		f := a.Field("model")
		if f == nil {
			t.Fatalf("%s has no model field", a.ID)
		}
		opts := f.Options(a.Values())
		own := 0
		for _, o := range opts {
			if strings.HasPrefix(o.Value, "opencode-go/") {
				own++
				if o.Direct != "OpenCode Go" {
					t.Errorf("%s: own model %s says it goes direct to %q, want OpenCode Go", a.ID, o.Value, o.Direct)
				}
			} else if o.Ref != "" && o.Direct != "" {
				t.Errorf("%s: magpie's %s said to go direct to %q", a.ID, o.Value, o.Direct)
			}
		}
		if own == 0 {
			t.Errorf("%s offered none of its own OpenCode Go models: %v", a.ID, pickRows(opts, ""))
		}
	}

	// what the tag says: picked, Pi asks OpenCode Go itself
	if err := pi(home).Field("model").Set("opencode-go/glm-5.1"); err != nil {
		t.Fatal(err)
	}
	if p, _ := edit.GetJSON(filepath.Join(piDir, "settings.json"), "defaultProvider"); p != "opencode-go" {
		t.Fatalf("defaultProvider %q", p)
	}
}
