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
	t.Cleanup(AntigravityBuiltinForTest()) // the retired built-in route
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
		for name, price := range map[string]func(string) (catalog.Price, bool){
			"MakerPrice": MakerPrice, "antigravity ListPrice": ag.ListPrice, "relay ListPrice": other.ListPrice} {
			w := want[base]
			if _, orig := antigravityOriginal[base]; name == "antigravity ListPrice" && orig {
				w = antigravityOriginal[base] // Antigravity counts Carbon and Argon at the original price
			}
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

// ownsh1p on X: Usage showed gemini-4-argon-trial's tokens and no cost.
// Google gives Gemini 4 Argon to AI Pro trials on eligible devices, the
// same model, so a gemini- id's -trial is priced as its model, at an
// effort after it too. Another vendor's -trial is its own offer and stays
// unpriced.
func TestGeminiTrialIDPricedAsItsModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	// models.dev's prices on 2026-11-16
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "google":{"id":"google","models":{
	    "gemini-4-argon":{"id":"gemini-4-argon","cost":{"input":2,"output":10,"cache_read":0.1}},
	    "gemini-4-carbon":{"id":"gemini-4-carbon","cost":{"input":2,"output":10,"cache_read":0.1}}}},
	  "xai":{"id":"xai","models":{
	    "grok-4.7":{"id":"grok-4.7","cost":{"input":3,"output":15}}}},
	  "zhipuai":{"id":"zhipuai","models":{
	    "glm-5.3":{"id":"glm-5.3","cost":{"input":1,"output":4}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	ag := Provider{ID: "antigravity", Name: "Antigravity"}
	for id, base := range map[string]string{
		"gemini-4-argon-trial":        "gemini-4-argon",
		"Gemini-4-Argon-Trial":        "gemini-4-argon",
		"google/gemini-4-argon-trial": "gemini-4-argon",
		"gemini-4-carbon-trial":       "gemini-4-carbon",
		"gemini-4-argon-trial-high":   "gemini-4-argon",
		"gemini-4-argon-high-trial":   "gemini-4-argon",
		// kekao on Discord: Antigravity's tiered id, at an effort too
		"gemini-4-argon-tiered":            "gemini-4-argon",
		"gemini-4-argon-tiered-high":       "gemini-4-argon",
		"gemini-4-argon-tiered-extra-high": "gemini-4-argon",
		"google/gemini-4-argon-tiered-low": "gemini-4-argon",
		"gemini-4-argon-trial-extra-high":  "gemini-4-argon",
	} {
		for name, price := range map[string]func(string) (catalog.Price, bool){
			"MakerPrice": MakerPrice, "antigravity ListPrice": ag.ListPrice} {
			pr, ok := price(id)
			in, out, cr := 2.0, 10.0, 0.1
			if name == "antigravity ListPrice" { // Antigravity counts Carbon and Argon at the original price
				in, out, cr = 4, 20, 0.2
			}
			if !ok || pr.Input != in || pr.Output != out || pr.CacheRead != cr {
				t.Errorf("%s(%q) = %+v, %v; want %s's $%v/$%v", name, id, pr, ok, base, in, out)
			}
		}
		if got := PricedName(id); got != base && got != "google/"+base { // a route's prefix stays
			t.Errorf("PricedName(%q) = %q, want %q", id, got, base)
		}
	}
	for _, id := range []string{"grok-4.7-trial", "glm-5.3-trial", "gemini-4-argon-trialx", "gemini-4-argon-trial-fast",
		"grok-4.7-tiered", "glm-5.3-tiered-high", "gemini-4-argon-tieredx", "gemini-4-argon-tiered-fast"} {
		if pr, ok := MakerPrice(id); ok {
			t.Errorf("MakerPrice(%q) = %+v; want unpriced", id, pr)
		}
	}
}

// kekao on Discord: gemini-4-hydrogen-tiered-high showed no price. It is
// Antigravity's tiered Gemini 4 Hydrogen at high, priced as the model.
func TestGeminiTieredIDPricedAsItsModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	// models.dev's price on 2026-11-25
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "google":{"id":"google","models":{
	    "gemini-4-hydrogen":{"id":"gemini-4-hydrogen","cost":{"input":1.5,"output":7.5,"cache_read":0.075}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	ag := Provider{ID: "antigravity", Name: "Antigravity"}
	for _, id := range []string{"gemini-4-hydrogen-tiered-high", "gemini-4-hydrogen-tiered"} {
		for name, price := range map[string]func(string) (catalog.Price, bool){
			"MakerPrice": MakerPrice, "antigravity ListPrice": ag.ListPrice} {
			if pr, ok := price(id); !ok || pr.Input != 1.5 || pr.Output != 7.5 || pr.CacheRead != 0.075 {
				t.Errorf("%s(%q) = %+v, %v; want $1.5/$7.5", name, id, pr, ok)
			}
		}
		if got := PricedName(id); got != "gemini-4-hydrogen" {
			t.Errorf("PricedName(%q) = %q", id, got)
		}
	}
}
