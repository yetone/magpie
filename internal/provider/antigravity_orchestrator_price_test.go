package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// writeAntigravityRolesCatalog is models.dev's three Gemini 4 models and
// another vendor's, in a sandbox HOME.
func writeAntigravityRolesCatalog(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	// models.dev's prices on 2026-11-15
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "google":{"id":"google","models":{
	    "gemini-4-argon":{"id":"gemini-4-argon","cost":{"input":2,"output":10,"cache_read":0.1}},
	    "gemini-4-carbon":{"id":"gemini-4-carbon","cost":{"input":2.5,"output":12,"cache_read":0.25}},
	    "gemini-4-hydrogen":{"id":"gemini-4-hydrogen","cost":{"input":1.5,"output":7.5,"cache_read":0.075}}}},
	  "openai":{"id":"openai","models":{
	    "gpt-5.1-codex":{"id":"gpt-5.1-codex","cost":{"input":1.25,"output":10}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// Antigravity's Orchestrator mode records its calls under role ids, not
// a model's: gemini-pro-orchestrator-argon, gemini-pro-orchestrator-carbon
// and the worker gemini-flash-worker-agy showed tokens with no cost
// (guyng35955 on X). Each is priced as the Gemini model Antigravity's
// docs say it is, whichever provider's call names it; a name only like
// one of them, and every other model, keeps its own price or none.
func TestAntigravityOrchestratorIDsPricedAsTheirModel(t *testing.T) {
	t.Cleanup(AntigravityBuiltinForTest())
	writeAntigravityRolesCatalog(t)
	// carbon is given its own price here, apart from argon's, so a role
	// priced as the other model shows
	want := map[string]catalog.Price{
		"gemini-4-argon":    {Input: 2, Output: 10, CacheRead: 0.1},
		"gemini-4-carbon":   {Input: 2.5, Output: 12, CacheRead: 0.25},
		"gemini-4-hydrogen": {Input: 1.5, Output: 7.5, CacheRead: 0.075},
	}
	ag := Provider{ID: "antigravity", Name: "Antigravity"}
	other := Provider{ID: "some-relay", Name: "Relay"}
	for id, model := range map[string]string{
		// the reporter's three
		"gemini-pro-orchestrator-argon":  "gemini-4-argon",
		"gemini-pro-orchestrator-carbon": "gemini-4-carbon",
		"gemini-flash-worker-agy":        "gemini-4-hydrogen",
		// Orchestrator (Carbon) before Argon came
		"gemini-pro-orchestrator": "gemini-4-carbon",
		// spelled as a route or an effort names them
		"Gemini-Pro-Orchestrator-Argon":        "gemini-4-argon",
		"google/gemini-pro-orchestrator-argon": "gemini-4-argon",
		"gemini-pro-orchestrator-carbon-high":  "gemini-4-carbon",
		"gemini-flash-worker-agy-extra-high":   "gemini-4-hydrogen",
	} {
		w := want[model]
		for name, price := range map[string]func(string) (catalog.Price, bool){
			"MakerPrice": MakerPrice, "antigravity ListPrice": ag.ListPrice, "relay ListPrice": other.ListPrice} {
			if pr, ok := price(id); !ok || !samePrice(pr, w) {
				t.Errorf("%s(%q) = %+v, %v; want %s's %+v", name, id, pr, ok, model, w)
			}
		}
		if got := PricedName(id); got != model {
			t.Errorf("PricedName(%q) = %q, want %q", id, got, model)
		}
	}
	// the models themselves, and another vendor's, keep their own price
	for id, w := range map[string]catalog.Price{
		"gemini-4-argon": want["gemini-4-argon"], "gemini-4-carbon": want["gemini-4-carbon"],
		"gpt-5.1-codex": {Input: 1.25, Output: 10},
	} {
		if pr, ok := MakerPrice(id); !ok || !samePrice(pr, w) {
			t.Errorf("MakerPrice(%q) = %+v, %v; want %+v", id, pr, ok, w)
		}
	}
	// a name only like a role is no role
	for _, id := range []string{"gemini-pro-orchestrator-neon", "gemini-flash-worker", "gemini-pro-orchestrator-argon-fast",
		"my-gemini-pro-orchestrator-argon", "gemini-flash-worker-agy-ultra", "orchestrator", "gpt-5.1-codex-orchestrator"} {
		if pr, ok := MakerPrice(id); ok {
			t.Errorf("MakerPrice(%q) = %+v; want unpriced", id, pr)
		}
		if pr, ok := other.ListPrice(id); ok {
			t.Errorf("relay ListPrice(%q) = %+v; want unpriced", id, pr)
		}
	}
}
