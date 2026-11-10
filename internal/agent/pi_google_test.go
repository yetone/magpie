package agent

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// #3282: Google plugin models use Code Assist internally, but Pi must
// send Gemini requests to the gateway, with its key rather than the plugin's.
func TestPiGooglePluginModels(t *testing.T) {
	home := syncHome(t)
	t.Setenv("MAGPIE_BUN", "")
	const spec = "google-plugin-test"
	writeFile(t, filepath.Join(settings.Dir(), "plugins.json"), `{"plugins":[{"spec":"`+spec+`"}]}`)
	writeFile(t, filepath.Join(settings.Dir(), "plugin-auth.json"), `{"google-plugin":{"type":"api","key":"plugin-secret"}}`)
	plugin.UseCached([]plugin.Provider{{ID: "google-plugin", Spec: spec, Name: "Google plugin",
		Models: []plugin.Model{{ID: "gemini-3-flash", Name: "Gemini 3 Flash", NPM: "@ai-sdk/google"}}}})
	t.Cleanup(plugin.Settle)
	const gw = "http://127.0.0.1:17325"
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:17325")
	for _, a := range []*Agent{pi(home), omo(home), primeAgent(home), stepcode(home)} {
		t.Run(a.ID, func(t *testing.T) {
			if err := a.Field("model").Set("magpie/google-plugin/gemini-3-flash"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(filepath.Dir(a.Path), "models.json")
			// Existing files must acquire the correction during normal sync too.
			p := providerAt(t, path, "providers")
			for _, raw := range p["models"].([]any) {
				m := raw.(map[string]any)
				delete(m, "api")
				delete(m, "baseUrl")
			}
			b, _ := json.Marshal(map[string]any{"providers": map[string]any{"magpie": p}})
			writeFile(t, path, string(b))
			if err := a.Sync(); err != nil {
				t.Fatal(err)
			}
			p = providerAt(t, path, "providers")
			if p["api"] != "openai-completions" || p["baseUrl"] != gw+"/v1" || p["apiKey"] == "plugin-secret" {
				t.Fatalf("provider defaults or gateway authentication changed: %v", p)
			}
			found := false
			for _, raw := range p["models"].([]any) {
				m := raw.(map[string]any)
				if m["id"] != "google-plugin/gemini-3-flash" {
					continue
				}
				found = true
				// StepCode has no google-generative-ai stream, and Pi's
				// folder is read by Raft, whose fetch pi-ai's Google
				// adapter refuses: Chat there (piAsked)
				if a.ID == "stepcode" || a.ID == "pi" {
					if m["api"] != nil || m["baseUrl"] != nil {
						t.Errorf("%s can't ask Gemini's API: %v", a.ID, m)
					}
				} else if m["api"] != "google-generative-ai" || m["baseUrl"] != gw+"/v1beta" {
					t.Errorf("Google model still inherits Chat: %v", m)
				}
			}
			if !found {
				t.Fatal("Google plugin model not offered")
			}
		})
	}
	for _, m := range pencilProviderJSON(gw)["models"].([]map[string]any) {
		if m["api"] != nil || m["baseUrl"] != nil {
			t.Errorf("Pencil no longer uses its provider's Chat API: %v", m)
		}
	}
}

// Pi's google-generative-ai resolves each level through thinkingLevelMap to
// minimal, low, medium or high and throws on any other name before it sends
// (pi-ai's resolveGoogleThinkingLevel), so a Gemini model's level by another
// name is hidden there, while the same levels on Chat stay as they were.
func TestPiGoogleThinkingLevelsAreGeminis(t *testing.T) {
	m := catalog.Model{ID: "gem/gemini-3-pro", Name: "Gemini 3 Pro", APIs: []string{"gemini"}, Efforts: []string{"none", "low", "high", "xhigh", "max"}}
	e := piModelJSON(m, "http://127.0.0.1:1", true)
	if e["api"] != "google-generative-ai" {
		t.Fatalf("api: %v", e["api"])
	}
	levels := e["thinkingLevelMap"].(map[string]any)
	for l, want := range map[string]any{"off": "none", "minimal": nil, "low": "low", "medium": nil, "high": "high", "xhigh": nil, "max": nil} {
		if levels[l] != want {
			t.Errorf("%s: %v, want %v", l, levels[l], want)
		}
	}
	if got := piSupported(e); !slices.Equal(got, []string{"off", "low", "high"}) {
		t.Errorf("Pi offers %v", got)
	}
	chat := piModelJSON(catalog.Model{ID: m.ID, Name: m.Name, Efforts: m.Efforts}, "http://127.0.0.1:1", true)
	if l := chat["thinkingLevelMap"].(map[string]any); l["xhigh"] != "xhigh" || l["max"] != "max" {
		t.Errorf("a Chat model lost its levels: %v", l)
	}
}

// Raft runs Pi's SDK on Pi's own agent folder and hands each request a
// fetch of its own, which pi-ai's Google adapter refuses before sending
// ("Custom fetch is not supported by the Google Generative AI adapter",
// yunfei9958 on X), so a Gemini model in Pi's models.json stays on Chat,
// and Pi offers it the levels Chat has; OmO and Prime Agent, whose
// folders are their own, keep Gemini's API.
func TestPiFolderKeepsGeminiOnChatForRaft(t *testing.T) {
	m := catalog.Model{ID: "gem/gemini-3.8-flash", Name: "Gemini 3.8 Flash", APIs: []string{"gemini"}, Efforts: []string{"low", "high", "xhigh"}}
	const gw = "http://127.0.0.1:1"
	for id, want := range map[string]any{"pi": nil, "stepcode": nil, "omo": "google-generative-ai", "prime-agent": "google-generative-ai", "mu": nil} {
		e := piModelJSON(piAsked(id, m), gw, true)
		if e["api"] != want {
			t.Errorf("%s: api %v, want %v", id, e["api"], want)
		}
		if want == nil && (e["baseUrl"] != nil || e["thinkingLevelMap"].(map[string]any)["xhigh"] != "xhigh") {
			t.Errorf("%s: not Chat's entry: %v", id, e)
		}
	}
	if !slices.Equal(m.APIs, []string{"gemini"}) {
		t.Errorf("the catalog's model changed: %v", m.APIs)
	}
}
