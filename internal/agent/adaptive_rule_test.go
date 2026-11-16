package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// #6778 (larryhaonlp): DSH's Xhigh reached agy-cli/gemini-4-hydrogen, on
// magpie's Anthropic route, as high. pi-ai asks a model on Messages for a
// budget unless its compat says forceAdaptiveThinking, and the budget path
// clamps xhigh and max to high's 16384, which agy-cli reads as high. A
// Gemini 3 or later is now told to think adaptively there, by the one rule
// dsh and Pi (both pi-ai) are written by; a Gemini 2.x, another route and
// a model that takes a budget are not.
func TestPiAgentsAskGeminiAdaptively(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"agy-cli/gemini-4-hydrogen", true},
		{"relay/gemini-3.8-flash", true},
		{"anth/claude-sonnet-5", true},
		{"zcode/GLM-5.3", true},
		{"relay/gemini-2.5-pro", false},
		{"kimi/kimi-k3", false},
		{"minimax/MiniMax-M3.1-Flash", false},
	} {
		m := catalog.Model{ID: tc.id, Context: 1048576, Output: 65536, APIs: []string{string(provider.Anthropic)}, Efforts: []string{"low", "medium", "high", "xhigh"}}
		for _, k := range dshKinds {
			got := dshKindConfig(k, []catalog.Model{m}, k.api, "http://127.0.0.1:3425").Models[0]
			adaptive := got.Compat != nil && got.Compat.ForceAdaptiveThinking
			if want := tc.want && k.api == "anthropic-messages"; adaptive != want {
				t.Errorf("dsh %s on %s: forceAdaptiveThinking %v, want %v", tc.id, k.api, adaptive, want)
			}
		}
		for _, on := range []string{"", "anthropic-messages", "openai-completions"} {
			e := piModelOn(m, "http://127.0.0.1:3425", true, on)
			c, _ := e["compat"].(map[string]any)
			if want := tc.want && on != "openai-completions"; (c["forceAdaptiveThinking"] == true) != want {
				t.Errorf("Pi %s on %q: compat %v, want forceAdaptiveThinking %v", tc.id, on, c, want)
			}
		}
	}
}

// A compat switch magpie writes is put as magpie has it, but a false one
// stands in the file only by the user's hand (magpie writes its switches
// as true alone), and stays. And what dsh's Models page set by hand on one
// of magpie's models goes into settings.yaml too: dsh imports that file
// into the profile it starts on, so one written without them took them
// out of the profile at each start (the reporter's compat went on
// restart).
func TestDshHandSetCompatOutlivesGeminiRuleAndRestart(t *testing.T) {
	home, dir, _ := dshRouteHome(t)
	dshKeyFixture(dir)
	if err := provider.Save(provider.Provider{ID: "anth", Name: "Anth", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"gemini-4-hydrogen", "gemini-4-carbon"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("anth", "http://127.0.0.1:1", []catalog.Model{
		{ID: "gemini-4-hydrogen", Efforts: []string{"low", "medium", "high", "xhigh"}},
		{ID: "gemini-4-carbon", Efforts: []string{"low", "medium", "high", "xhigh"}},
	}); err != nil {
		t.Fatal(err)
	}
	desktop := filepath.Join(dir, "profiles", "desktop", "cordis.patch.yml")
	os.MkdirAll(filepath.Dir(desktop), 0o755)
	os.WriteFile(desktop, []byte("# Your patch layer for this dsh profile.\n"+
		"- id: llm-pi-ai\n  name: \"@deepseek-ai/dsh-llm-pi-ai\"\n  config:\n    providers:\n"+
		"      magpie-anthropic:\n        displayName: Magpie · Anthropic\n        apiKeyEnv: "+dshKeyRef+"\n        api: anthropic-messages\n        baseURL: http://127.0.0.1:3425\n        models:\n"+
		"          - id: anth/gemini-4-hydrogen\n            compat:\n              forceAdaptiveThinking: false\n"+
		"          - id: anth/gemini-4-carbon\n            compat:\n              maxTokensField: max_tokens\n"+
		"- id: agent-default-model\n  config:\n    provider: magpie-anthropic\n    model: anth/gemini-4-hydrogen\n"), 0o644)
	a := dsh(home)
	for i := 1; i <= 2; i++ {
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
		_, compat := dshRouteCompat(t, desktop, "magpie-anthropic")
		if c := compat["anth/gemini-4-hydrogen"]; c["forceAdaptiveThinking"] != false {
			b, _ := os.ReadFile(desktop)
			t.Fatalf("sync %d: a forceAdaptiveThinking turned off by hand was put back on: %v\n%s", i, compat, b)
		}
		if c := compat["anth/gemini-4-carbon"]; c["forceAdaptiveThinking"] != true || c["maxTokensField"] != "max_tokens" {
			b, _ := os.ReadFile(desktop)
			t.Fatalf("sync %d: carbon's compat: %v\n%s", i, compat, b)
		}
		// dsh eats settings.yaml at each start; magpie writes it again
		raw, err := os.ReadFile(dshSettings(dir))
		if err != nil {
			t.Fatal(err)
		}
		s := string(raw)
		for _, want := range []string{"forceAdaptiveThinking: false", "maxTokensField: max_tokens", "forceAdaptiveThinking: true"} {
			if !strings.Contains(s, want) {
				t.Fatalf("sync %d: settings.yaml, which dsh imports into the profile at its start, lacks %q:\n%s", i, want, s)
			}
		}
		os.Rename(dshSettings(dir), dshSettings(dir)+".imported")
	}
}
