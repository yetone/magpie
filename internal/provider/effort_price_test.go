package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// An id named at a reasoning effort is priced as its model, whichever
// provider serves it: Antigravity's gemini-4-argon-high,
// gemini-4-carbon-high and claude-opus-5-5-high showed "price unknown"
// (stockAAAAA on X), as only Grok's ids were read this way. An id a
// catalog prices by its own name keeps that price, and a suffix that is
// part of a model's own name (-max, mistral-medium) is not taken for an
// effort of another model.
func TestEffortIDPricedAsItsModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	// models.dev's prices on 2026-11-13
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "google":{"id":"google","models":{
	    "gemini-4-argon":{"id":"gemini-4-argon","cost":{"input":2,"output":10,"cache_read":0.1}},
	    "gemini-4-carbon":{"id":"gemini-4-carbon","cost":{"input":2,"output":10,"cache_read":0.1}}}},
	  "anthropic":{"id":"anthropic","models":{
	    "claude-opus-5-5":{"id":"claude-opus-5-5","cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5}}}},
	  "openai":{"id":"openai","models":{
	    "gpt-5.1-codex":{"id":"gpt-5.1-codex","cost":{"input":1.25,"output":10}},
	    "o3-mini":{"id":"o3-mini","cost":{"input":1.1,"output":4.4}},
	    "o3-mini-high":{"id":"o3-mini-high","cost":{"input":3,"output":12}}}},
	  "mistral":{"id":"mistral","models":{
	    "vision":{"id":"vision","cost":{"input":9,"output":9}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	ag := Provider{ID: "antigravity", Name: "Antigravity"}
	other := Provider{ID: "some-relay", Name: "Relay"}
	want := map[string]catalog.Price{
		"gemini-4-argon":  {Input: 2, Output: 10, CacheRead: 0.1},
		"gemini-4-carbon": {Input: 2, Output: 10, CacheRead: 0.1},
		"claude-opus-5-5": {Input: 4, Output: 20, CacheRead: 0.2, CacheWrite: 5},
	}
	for id, base := range map[string]string{
		// the reporter's three
		"gemini-4-argon-high":  "gemini-4-argon",
		"gemini-4-carbon-high": "gemini-4-carbon",
		"claude-opus-5-5-high": "claude-opus-5-5",
		// the other words an effort is spelled in
		"gemini-4-argon-low": "gemini-4-argon", "gemini-4-argon-medium": "gemini-4-argon",
		"gemini-4-argon-minimal": "gemini-4-argon", "claude-opus-5-5-xhigh": "claude-opus-5-5",
		"gemini-4-carbon-extra-high": "gemini-4-carbon", "gemini-4-carbon-extra-low": "gemini-4-carbon",
		"Claude-Opus-5-5-High": "claude-opus-5-5", "google/gemini-4-argon-high": "gemini-4-argon",
	} {
		w := want[base]
		for name, price := range map[string]func(string) (catalog.Price, bool){
			"MakerPrice": MakerPrice, "antigravity ListPrice": ag.ListPrice, "relay ListPrice": other.ListPrice} {
			pr, ok := price(id)
			if !ok || pr.Input != w.Input || pr.Output != w.Output || pr.CacheRead != w.CacheRead || pr.CacheWrite != w.CacheWrite {
				t.Errorf("%s(%q) = %+v, %v; want %s's %+v", name, id, pr, ok, base, w)
			}
		}
	}
	if got := PricedName("gemini-4-argon-high"); got != "gemini-4-argon" {
		t.Errorf("PricedName(gemini-4-argon-high) = %q", got)
	}
	// an id priced by its own name keeps its own price
	if pr, ok := MakerPrice("o3-mini-high"); !ok || pr.Input != 3 || pr.Output != 12 {
		t.Errorf("MakerPrice(o3-mini-high) = %+v, %v; want its own $3/$12", pr, ok)
	}
	// -max is a model of its own, and a word size names no effort
	for _, id := range []string{"gpt-5.1-codex-max", "gpt-5.1-codex-none", "vision-medium",
		"mistral/vision-high", "gemini-4-argon-high-fast", "gemini-4-argon-ultra"} {
		if pr, ok := MakerPrice(id); ok {
			t.Errorf("MakerPrice(%q) = %+v; want unpriced", id, pr)
		}
		if pr, ok := other.ListPrice(id); ok {
			t.Errorf("ListPrice(%q) = %+v; want unpriced", id, pr)
		}
	}
}
