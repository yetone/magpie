package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

func TestOpenCodeVariants(t *testing.T) {
	for _, c := range []struct {
		efforts []string
		want    map[string]any
	}{
		{[]string{"none", "high", "max"}, map[string]any{
			"none": map[string]any{"reasoningEffort": "none"},
			"high": map[string]any{"reasoningEffort": "high"},
			"max":  map[string]any{"reasoningEffort": "max"},
		}},
		{[]string{"low", "medium", "high", "xhigh"}, map[string]any{
			"low":    map[string]any{"reasoningEffort": "low"},
			"medium": map[string]any{"reasoningEffort": "medium"},
			"high":   map[string]any{"reasoningEffort": "high"},
			"xhigh":  map[string]any{"reasoningEffort": "xhigh"},
		}},
		// none at all: an empty set, so OpenCode 2 doesn't make low, medium
		// and high of its own
		{nil, map[string]any{}},
	} {
		if got := openCodeVariants(c.efforts); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: got %v, want %v", c.efforts, got, c.want)
		}
	}
}

// TestOpenCodeWritesVariants: each model of magpie's in opencode.json names
// the reasoning levels it has as variants — OpenCode 2 otherwise offers low,
// medium and high for every one — and the rest of the entry is as before.
func TestOpenCodeWritesVariants(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{
		ID: "think", Name: "Think", Chat: "https://example.test/v1", Key: "key",
		Models: []string{"deep", "wide", "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("think", "https://example.test/v1", []catalog.Model{
		{ID: "deep", Efforts: []string{"none", "high", "max"}, Context: 1000000, Output: 64000},
		{ID: "wide", Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
		{ID: "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	writeFile(t, path, `{"theme":"dark"}`)
	if err := opencode(home, filepath.Join(home, ".config")).Field("model").Set("magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Theme    string `json:"theme"`
		Model    string `json:"model"`
		Provider map[string]struct {
			NPM    string                    `json:"npm"`
			Models map[string]map[string]any `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "dark" || cfg.Model != "magpie/think/deep" {
		t.Fatalf("config: %s", b)
	}
	p := cfg.Provider["magpie"]
	if p.NPM != "@ai-sdk/openai-compatible" {
		t.Fatalf("npm %q", p.NPM)
	}
	levels := func(id string) []string {
		t.Helper()
		m, ok := p.Models[id]
		if !ok {
			t.Fatalf("%s missing: %s", id, b)
		}
		vs, ok := m["variants"].(map[string]any)
		if !ok {
			t.Fatalf("%s has no variants: %v", id, m)
		}
		var out []string
		for _, e := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
			if v, ok := vs[e]; ok {
				if !reflect.DeepEqual(v, map[string]any{"reasoningEffort": e}) {
					t.Fatalf("%s %s: %v", id, e, v)
				}
				out = append(out, e)
			}
		}
		if len(out) != len(vs) {
			t.Fatalf("%s: unexpected variants %v", id, vs)
		}
		return out
	}
	if got := levels("think/deep"); !reflect.DeepEqual(got, []string{"none", "high", "max"}) {
		t.Fatalf("deep: %v", got)
	}
	if got := levels("think/wide"); !reflect.DeepEqual(got, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("wide: %v", got)
	}
	if got := levels("think/plain"); got != nil {
		t.Fatalf("plain: %v", got)
	}
	if lim, _ := p.Models["think/deep"]["limit"].(map[string]any); lim["context"] != float64(1000000) {
		t.Fatalf("deep limit: %v", p.Models["think/deep"])
	}
}

// Xiaomi's mimo-v2.6-flash, which models.dev lists under Xiaomi with a
// thinking switch alone, gets no variants in opencode.json — under Xiaomi's
// preset or a relay of the user's alike — and no thinking levels in Pi:
// OpenCode offered it the resellers' levels up to max, which Xiaomi turns
// away with a 400 (#214). A model no maker lists keeps the resellers'.
func TestOpenCodeNoResellerLevelsForSwitchModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "xiaomi": {"models": {"mimo-v2.6-flash": {"id":"mimo-v2.6-flash","reasoning":true,"reasoning_options":[{"type":"toggle"}]}}},
	  "llmgateway": {"models": {
	    "deepinfra/mimo-v2.6-flash": {"id":"deepinfra/mimo-v2.6-flash","reasoning_options":[{"type":"effort","values":["none","minimal","low","medium","high","xhigh","max"]}]},
	    "xiaomi/mimo-v2.6-flash": {"id":"xiaomi/mimo-v2.6-flash","reasoning_options":[{"type":"effort","values":["none","minimal","low","medium","high","xhigh","max"]}]},
	    "open-model": {"id":"open-model","reasoning_options":[{"type":"effort","values":["low","high","max"]}]}}}
	}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	xiaomi, err := provider.FromPreset("xiaomi")
	if err != nil {
		t.Fatal(err)
	}
	xiaomi.Key, xiaomi.Models = "k", []string{"mimo-v2.6-flash"}
	if err := provider.Save(xiaomi); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "k",
		Models: []string{"mimo-v2.6-flash", "open-model", "mystery-model"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	writeFile(t, path, `{}`)
	if err := opencode(home, filepath.Join(home, ".config")).Field("model").Set("magpie/xiaomi/mimo-v2.6-flash"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Provider map[string]struct {
			Models map[string]struct {
				Variants map[string]any `json:"variants"`
			} `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	ms := cfg.Provider["magpie"].Models
	for id, want := range map[string]int{"xiaomi/mimo-v2.6-flash": 0, "relay/mimo-v2.6-flash": 0, "relay/open-model": 3} {
		m, ok := ms[id]
		if !ok || m.Variants == nil || len(m.Variants) != want {
			t.Errorf("%s: variants %v (listed %v), want %d", id, m.Variants, ok, want)
		}
	}
	pi, _ := json.Marshal(magpieProviderJSON("pi"))
	var piCfg struct {
		Models []struct {
			ID        string         `json:"id"`
			Reasoning bool           `json:"reasoning"`
			Levels    map[string]any `json:"thinkingLevelMap"`
		} `json:"models"`
	}
	json.Unmarshal(pi, &piCfg)
	seen := 0
	for _, m := range piCfg.Models {
		switch m.ID {
		case "xiaomi/mimo-v2.6-flash", "relay/mimo-v2.6-flash":
			seen++
			if m.Reasoning || m.Levels != nil {
				t.Errorf("pi %s: reasoning %v, levels %v", m.ID, m.Reasoning, m.Levels)
			}
		case "relay/open-model":
			// the full map, levels the model lacks null so Pi hides them (#243)
			seen++
			want := map[string]any{"off": nil, "minimal": nil, "low": "low", "medium": nil, "high": "high", "xhigh": nil, "max": "max"}
			if !m.Reasoning || !reflect.DeepEqual(m.Levels, want) {
				t.Errorf("pi %s: reasoning %v, levels %v", m.ID, m.Reasoning, m.Levels)
			}
		case "relay/mystery-model":
			// levels magpie doesn't know: Pi's own, as before
			seen++
			if m.Reasoning || m.Levels != nil {
				t.Errorf("pi %s: reasoning %v, levels %v", m.ID, m.Reasoning, m.Levels)
			}
		}
	}
	if seen != 4 {
		t.Errorf("pi models: %s", pi)
	}
}
