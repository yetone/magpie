package provider

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// #1215: a provider at OpenCode's gateway saved with one URL gets the
// other APIs of that gateway's, and the models.dev list of the gateway its
// URL is at says which API each model goes on — Go's list for Go and
// Zen's for Zen, even when it was saved with the other's catalog. A URL
// or catalog the user gave stays theirs, and other hosts are left alone.
// #2168: Gemini's generateContent is one of them — Zen's at /zen/v1, Go's
// at the inference API's /inference/go/v1 (/zen/go/v1 has no such route)
// — and a model listed for Google's SDK goes on it.
func TestOpenCodeEndpointsFilledIn(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	os.MkdirAll(filepath.Join(cache, "magpie"), 0o755)
	// as models.dev lists them (2026-10-08, Gemini 2026-10-14): MiniMax
	// M2.7 is Anthropic's SDK on Go and the OpenAI-compatible one on Zen
	if err := os.WriteFile(catalog.CachePath(), []byte(`{
	  "opencode-go": {"models": {
	    "gpt-6-luna": {"id":"gpt-6-luna","provider":{"npm":"@ai-sdk/openai"}},
	    "minimax-m2.7": {"id":"minimax-m2.7","provider":{"npm":"@ai-sdk/anthropic"}},
	    "gemini-4-argon-contributor": {"id":"gemini-4-argon-contributor","provider":{"npm":"@ai-sdk/google"}},
	    "glm-5.3": {"id":"glm-5.3"}}},
	  "opencode": {"models": {
	    "claude-opus-5": {"id":"claude-opus-5","provider":{"npm":"@ai-sdk/anthropic"}},
	    "gemini-3-flash": {"id":"gemini-3-flash","provider":{"npm":"@ai-sdk/google"}},
	    "minimax-m2.7": {"id":"minimax-m2.7"}}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	const zg, zen, igo, inf = "https://opencode.ai/zen/go", "https://opencode.ai/zen", "https://opencode.ai/inference/go", "https://opencode.ai/inference"
	for _, tc := range []struct {
		name                                       string
		in                                         Provider
		chat, responses, anthropic, gemini, catlog string
		apis                                       map[string]Protocol // "" for none named
	}{
		{"go, chat alone", Provider{ID: "opencode-go", Chat: zg + "/v1"},
			zg + "/v1", zg + "/v1", zg, igo + "/v1", "opencode-go",
			map[string]Protocol{"gpt-6-luna": Responses, "minimax-m2.7": Anthropic, "gemini-4-argon-contributor": Gemini, "glm-5.3": ""}},
		{"go, anthropic alone with /v1", Provider{ID: "ocgo", Anthropic: zg + "/v1"},
			zg + "/v1", zg + "/v1", zg, igo + "/v1", "opencode-go",
			map[string]Protocol{"gpt-6-luna": Responses}},
		{"go at the inference API", Provider{ID: "ocgo", Chat: igo + "/v1"},
			igo + "/v1", igo + "/v1", igo, igo + "/v1", "opencode-go",
			map[string]Protocol{"gemini-4-argon-contributor": Gemini, "glm-5.3": ""}},
		{"zen saved with go's catalog", Provider{ID: "opencode-go", Catalog: "opencode-go", Chat: zen + "/v1"},
			zen + "/v1", zen + "/v1", zen, zen + "/v1", "opencode-go",
			map[string]Protocol{"claude-opus-5": Anthropic, "gemini-3-flash": Gemini, "minimax-m2.7": ""}},
		{"zen at the inference API", Provider{ID: "oc", Chat: inf},
			inf, inf + "/v1", inf, inf + "/v1", "opencode",
			map[string]Protocol{"gemini-3-flash": Gemini}},
		{"a URL of the user's own stays", Provider{ID: "oc", Chat: zg + "/v1", Responses: "https://proxy.test/go/v1", Gemini: "https://proxy.test/gemini"},
			zg + "/v1", "https://proxy.test/go/v1", zg, "https://proxy.test/gemini", "opencode-go", nil},
		{"another relay", Provider{ID: "relay", Catalog: "opencode-go", Chat: "https://relay.test/v1"},
			"https://relay.test/v1", "", "", "", "opencode-go",
			map[string]Protocol{"gpt-6-luna": ""}},
		{"another path at opencode.ai", Provider{ID: "oc-other", Chat: "https://opencode.ai/other/v1"},
			"https://opencode.ai/other/v1", "", "", "", "", nil},
	} {
		p := normalize(tc.in)
		if p.Chat != tc.chat || p.Responses != tc.responses || p.Anthropic != tc.anthropic || p.Gemini != tc.gemini || p.Catalog != tc.catlog {
			t.Errorf("%s: chat %q responses %q anthropic %q gemini %q catalog %q", tc.name, p.Chat, p.Responses, p.Anthropic, p.Gemini, p.Catalog)
		}
		for model, want := range tc.apis {
			got := p.APIs(model)
			if want == "" && got != nil || want != "" && !slices.Equal(got, []Protocol{want}) {
				t.Errorf("%s: %s APIs %v, want %q", tc.name, model, got, want)
			}
			// a Gemini model has no API of the three a client asks it on
			// as it is: every request to it is translated
			native := want
			if want == Gemini {
				native = ""
			}
			if want != "" && p.Native(model) != native {
				t.Errorf("%s: %s native %q, want %q", tc.name, model, p.Native(model), native)
			}
		}
	}
}
