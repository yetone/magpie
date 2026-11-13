package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A custom provider serving a model models.dev knows from others (a
// Volcengine endpoint's glm-5.3-flash) hands Codex the reasoning levels
// those give it: the catalog entry had none, so the Codex app offered no
// effort and magpie's own control showed a level Codex never used.
func TestCodexCustomProviderModelHasEfforts(t *testing.T) {
	home, read := codexHome(t, "", "model_reasoning_effort = \"none\"\n")
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "zai":{"models":{"glm-5.3-flash":{"id":"glm-5.3-flash","reasoning_options":[{"type":"effort","values":["low","high","max"]}]},
	                   "glm-4.5-air":{"id":"glm-4.5-air"}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := provider.Save(provider.Provider{ID: "volc", Name: "Volc", Key: "k", Chat: "http://127.0.0.1:1/api/v3",
		Models: []string{"glm-5.3-flash", "glm-4.5-air"}}); err != nil {
		t.Fatal(err)
	}
	cx := codex(home)
	if err := cx.Fields[0].Set("volc/glm-5.3-flash"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_reasoning_effort = "low"`) {
		t.Fatalf("effort not settled on the nearest level:\n%s", cfg)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".codex", "magpie-models.json"))
	var cat struct {
		Models []struct {
			Slug    string  `json:"slug"`
			Default *string `json:"default_reasoning_level"`
			Levels  []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &cat); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range cat.Models {
		var ls []string
		for _, l := range m.Levels {
			ls = append(ls, l.Effort)
		}
		d := ""
		if m.Default != nil {
			d = *m.Default
		}
		got[m.Slug] = strings.Join(ls, ",") + " / " + d
	}
	// Ultra after its own levels: Codex sends a model's top level for it
	// (codexcat.ultraHere; pingqipai3 on X)
	if got["volc/glm-5.3-flash"] != "low,high,max,ultra / low" {
		t.Errorf("glm-5.3-flash: %q", got["volc/glm-5.3-flash"])
	}
	// a model no one says reasons keeps none
	if got["volc/glm-4.5-air"] != " / " {
		t.Errorf("glm-4.5-air: %q", got["volc/glm-4.5-air"])
	}
	effort := cx.Fields[1]
	var opts []string
	ef := effort.Options(map[string]string{"model": "volc/glm-5.3-flash"})
	for _, o := range ef {
		opts = append(opts, o.Value)
	}
	if strings.Join(opts, ",") != ",low,high,max" {
		t.Errorf("effort options: %v", opts)
	}
	// Default says what Codex takes then: the catalog's default
	if ef[0].Takes != "high" {
		t.Errorf("default takes %q", ef[0].Takes)
	}

	// unset, the control shows Default, which is what was picked
	if err := effort.Set(""); err != nil {
		t.Fatal(err)
	}
	if v := effort.Get(); v != "" {
		t.Errorf("unset effort shows %q", v)
	}

	// a level the model doesn't take, left from before, is settled when
	// the catalog is synced
	if err := effort.Set("none"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_reasoning_effort = "low"`) {
		t.Errorf("sync left the effort:\n%s", cfg)
	}
}

// Default on Codex's effort control stays Default (lgtm on Discord: picked
// on Codex · WSL Ubuntu, it turned into medium): an effort left unset is
// not written in when a model is picked or the catalog synced, and the
// control reads it as Default, with the level Codex then takes beside it —
// for Codex's own model, its own default (gpt-6.1-sol's low), not medium.
func TestCodexDefaultEffortStaysDefault(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"models":[
		{"slug":"gpt-6.1-sol","display_name":"GPT-6.1 Sol","priority":1,"default_reasoning_level":"low",
		 "supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}]}]}`), 0o644)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "zai":{"models":{"glm-5.3-flash":{"id":"glm-5.3-flash","reasoning_options":[{"type":"effort","values":["low","high","max"]}]}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := provider.Save(provider.Provider{ID: "volc", Name: "Volc", Key: "k", Chat: "http://127.0.0.1:1/api/v3",
		Models: []string{"glm-5.3-flash"}}); err != nil {
		t.Fatal(err)
	}
	cx := codex(home)
	model, effort := cx.Fields[0], cx.Fields[1]
	unset := func(when string) {
		t.Helper()
		if cfg := read(); strings.Contains(cfg, "model_reasoning_effort") {
			t.Errorf("%s: Default written over:\n%s", when, cfg)
		}
		if v := effort.Get(); v != "" {
			t.Errorf("%s: Default shows as %q", when, v)
		}
	}
	if err := model.Set("volc/glm-5.3-flash"); err != nil {
		t.Fatal(err)
	}
	unset("magpie's model picked")
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	unset("catalog synced")
	if err := model.Set("gpt-6.1-sol"); err != nil {
		t.Fatal(err)
	}
	unset("Codex's own model picked")
	if d := effort.Options(map[string]string{"model": "gpt-6.1-sol"})[0]; d.Value != "" || d.Takes != "low" {
		t.Errorf("Codex's own model's default: %+v", d)
	}

	// a level set that the model doesn't take is still settled
	if err := effort.Set("max"); err != nil {
		t.Fatal(err)
	}
	if err := model.Set("gpt-6.1-sol"); err != nil {
		t.Fatal(err)
	}
	if v := effort.Get(); v == "max" || v == "" {
		t.Errorf("max left on a model without it: %q", v)
	}
}
