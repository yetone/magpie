package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// useKimiCodeCatalog puts testdata/kimi_code_catalog.json in the catalog
// cache: models.dev's own entries (2026-10-09) for moonshotai and both Kimi
// Code plans, which list k3, k3-256k, kimi-for-coding and
// kimi-for-coding-highspeed at $0, beside gpt-6-luna and a free OpenCode
// model; plus a relay's catalog that lists a "k3" of its own at $0.
func useKimiCodeCatalog(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	b, err := os.ReadFile(filepath.Join("testdata", "kimi_code_catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["freebie"] = map[string]any{"id": "freebie", "models": map[string]any{
		"k3": map[string]any{"id": "k3", "cost": map[string]any{"input": 0, "output": 0}}}}
	b, _ = json.Marshal(m)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// #1370: a Kimi Code membership's models are priced at the Kimi API model
// each is, not at the $0 its plan's models.dev catalog lists, and
// kimi-for-coding (K2.8 Preview, not sold on the API) is unpriced rather
// than free. Every other provider's price is what it was.
func TestKimiCodeListPrice(t *testing.T) {
	useKimiCodeCatalog(t)
	k3 := catalog.Price{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3}
	highspeed := catalog.Price{Input: 1.9, Output: 8, CacheRead: 0.38}
	for _, preset := range []string{"kimi-code", "kimi-code-cn"} {
		p, err := FromPreset(preset)
		if err != nil {
			t.Fatal(err)
		}
		for model, want := range map[string]catalog.Price{"k3": k3, "K3": k3, "k3-256k": k3, "kimi-for-coding-highspeed": highspeed} {
			if pr, ok := p.ListPrice(model); !ok || !pr.Same(want) {
				t.Errorf("%s %s: %+v %v, want %+v", preset, model, pr, ok, want)
			}
		}
		if pr, ok := p.ListPrice("kimi-for-coding"); ok {
			t.Errorf("%s kimi-for-coding priced at %+v: the API sells no K2.8 Preview", preset, pr)
		}
	}

	// the API's own kimi-k3, at Kimi's pay-as-you-go endpoint, is unchanged
	moon, _ := FromPreset("moonshot")
	if pr, ok := moon.ListPrice("kimi-k3"); !ok || !pr.Same(k3) {
		t.Errorf("moonshot kimi-k3: %+v %v", pr, ok)
	}
	// a relay's own k3 at $0 stays $0: the alias is Kimi Code's alone
	relay := Provider{ID: "relay", Catalog: "freebie"}
	if pr, ok := relay.ListPrice("k3"); !ok || !pr.Same(catalog.Price{}) {
		t.Errorf("relay k3: %+v %v, want its own $0", pr, ok)
	}
	// a model listed free elsewhere is still free, and Codex's still at its maker's
	zen := Provider{ID: "zen", Catalog: "opencode"}
	if pr, ok := zen.ListPrice("qwen3.6-plus-free"); !ok || !pr.Same(catalog.Price{}) {
		t.Errorf("opencode free model: %+v %v", pr, ok)
	}
	if pr, ok := (Provider{ID: "codex"}).ListPrice("gpt-6-luna"); !ok || pr.Input != 0.1 || pr.Output != 0.5 {
		t.Errorf("codex gpt-6-luna: %+v %v", pr, ok)
	}
	// with the provider gone, a bare k3 has no maker price: no $0 either
	if pr, ok := MakerPrice("k3"); ok {
		t.Errorf("MakerPrice(k3) = %+v from a membership catalog", pr)
	}

	// the Requests list names the API model a k3 is priced as, a relay's k3 none
	kc, _ := FromPreset("kimi-code-cn")
	kc.Key = "sk-test"
	relay.Name, relay.Key, relay.Chat = "Relay", "sk-test", "https://relay.example/v1"
	for _, p := range []Provider{kc, relay} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if got := PricedNameFor("kimi-code-cn", "k3-256k"); got != "kimi-k3" {
		t.Errorf("PricedNameFor(kimi-code-cn, k3-256k) = %q", got)
	}
	if got := PricedNameFor("relay", "k3"); got != "k3" {
		t.Errorf("PricedNameFor(relay, k3) = %q", got)
	}
	if got := PricedNameFor("codex", "codex-auto-review"); got != PricedName("codex-auto-review") {
		t.Errorf("PricedNameFor(codex, codex-auto-review) = %q", got)
	}
}
