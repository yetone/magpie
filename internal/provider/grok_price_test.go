package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
)

// A Grok id named at an effort is priced as its model; a fast, mini or
// build one no catalog lists stays unpriced (#224).
func TestGrokEffortListPrice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "xai":{"id":"xai","models":{
	    "grok-4.7":{"id":"grok-4.7","cost":{"input":2,"output":6,"cache_read":0.5}},
	    "grok-4.6":{"id":"grok-4.6","cost":{"input":2,"output":6,"cache_read":0.5}}}},
	  "openai":{"id":"openai","models":{
	    "gpt-6-astra":{"id":"gpt-6-astra","cost":{"input":10,"output":50}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	grok := Provider{ID: "grok", Name: "Grok (SuperGrok)"}
	for _, id := range []string{"grok-4.7", "grok-4.7-low", "grok-4.7-medium", "grok-4.7-high", "grok-4.7-xhigh",
		"GROK-4.7-High", "x-ai/grok-4.7-xhigh", "grok-4.6-minimal",
		// a model at a window of its own (#1608)
		"grok-4.7@500k", "grok-4.6@500k"} {
		for name, price := range map[string]func(string) (catalog.Price, bool){"MakerPrice": MakerPrice, "ListPrice": grok.ListPrice} {
			pr, ok := price(id)
			if !ok || pr.Input != 2 || pr.Output != 6 || pr.CacheRead != 0.5 {
				t.Errorf("%s(%q) = %+v, %v; want grok's $2/$6/$0.5", name, id, pr, ok)
			}
		}
	}
	for _, id := range []string{"grok-4.7-fast", "grok-4.7-low-fast", "grok-4.7-xhigh-fast", "grok-4.7-build",
		"grok-4.7-build-fast", "grok-4.7-mini", "grok-4.7-mini-high", "grok-4.7-max",
		// only Grok's ids are read this way
		"gpt-6-astra-high", "cursor-grok-4.7-high", "grok-4.7-build@500k"} {
		if pr, ok := grok.ListPrice(id); ok {
			t.Errorf("ListPrice(%q) = %+v; want unpriced", id, pr)
		}
		if pr, ok := MakerPrice(id); ok {
			t.Errorf("MakerPrice(%q) = %+v; want unpriced", id, pr)
		}
	}
	// a window is read off any id, not only Grok's: the Cursor plugin's
	// sized ids (TestCursorWindowIDsPricedAsTheirModel)
	if pr, ok := grok.ListPrice("gpt-6-astra@500k"); !ok || pr.Input != 10 || pr.Output != 50 {
		t.Errorf("ListPrice(gpt-6-astra@500k) = %+v, %v; want gpt-6-astra's $10/$50", pr, ok)
	}
	if got := PricedName("grok-4.7-xhigh"); got != "grok-4.7" {
		t.Errorf("PricedName(grok-4.7-xhigh) = %q", got)
	}
	if got := PricedName("grok-4.7-low-fast"); got != "grok-4.7-low-fast" {
		t.Errorf("PricedName(grok-4.7-low-fast) = %q", got)
	}
}

// A Cursor account's fast Grok ids are priced at what Cursor bills fast
// mode (https://cursor.com/docs/models: Grok 4.7 (Fast) $4 / $1 cache
// read / $12, 3x standard past 256k), whether the built-in or the plugin
// serves them, so a routing group of grok-4.7-fast counts its cost
// (TradingLife on Discord). A "-fast" Grok id elsewhere is a model of its
// own, and stays as the catalogs price it.
func TestCursorGrokFastPrice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "xai":{"id":"xai","models":{
	    "grok-4.7":{"id":"grok-4.7","cost":{"input":2,"output":6,"cache_read":0.5}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	builtin := Provider{ID: "cursor", Name: "Cursor", Account: &Account{Agent: "cursor"}}
	moved := Provider{ID: "cursor", Name: "Cursor", Account: &Account{plugin: &plugin.Provider{ID: "cursor"}}}
	for _, p := range []Provider{builtin, moved} {
		for _, id := range []string{"grok-4.7-fast", "grok-4.7-low-fast", "grok-4.7-high-fast", "grok-4.7-xhigh-fast",
			"GROK-4.7-Fast", "grok-4.7-fast@500k", "grok-4.7-medium-fast@256k"} {
			pr, ok := p.ListPrice(id)
			if !ok || pr.Input != 4 || pr.Output != 12 || pr.CacheRead != 1 {
				t.Errorf("%s ListPrice(%q) = %+v, %v; want Cursor's fast $4/$12/$1", p.PluginProvider(), id, pr, ok)
				continue
			}
			// past 256k, 3x standard
			if long := pr.At(300000); long.Input != 6 || long.Output != 18 || long.CacheRead != 1.5 {
				t.Errorf("%s ListPrice(%q) past 256k = %+v; want $6/$18/$1.5", p.PluginProvider(), id, long)
			}
			if short := pr.At(200000); short.Input != 4 {
				t.Errorf("%s ListPrice(%q) at 200k = %+v; want $4", p.PluginProvider(), id, short)
			}
		}
		// not fast: grok's own price
		if pr, ok := p.ListPrice("grok-4.7-high"); !ok || pr.Input != 2 {
			t.Errorf("ListPrice(grok-4.7-high) = %+v, %v; want $2", pr, ok)
		}
		// a fast id of a model Cursor gives no fast price for
		if pr, ok := p.ListPrice("grok-4.6-fast"); ok {
			t.Errorf("ListPrice(grok-4.6-fast) = %+v; want unpriced", pr)
		}
	}
	// elsewhere a fast id is not Cursor's fast mode (Grok's own
	// grok-4.7-build-fast is priced as Grok's, TestGrokBuildFastPrice)
	grok := Provider{ID: "grok", Name: "Grok", Account: &Account{plugin: &plugin.Provider{ID: "grok"}}}
	for _, id := range []string{"grok-4.7-fast", "grok-4.7-low-fast"} {
		if pr, ok := grok.ListPrice(id); ok {
			t.Errorf("grok ListPrice(%q) = %+v; want unpriced", id, pr)
		}
	}
	for _, id := range []string{"grok-4.7-fast", "grok-4.7-build-fast"} {
		if pr, ok := MakerPrice(id); ok {
			t.Errorf("MakerPrice(%q) = %+v; want unpriced", id, pr)
		}
	}
}

// A window the id names without a k (the grok plugin writes a window that
// no multiple of 1000 is as bare digits) is priced as its model, as one
// spelled with a k is.
func TestGrokWindowWithoutKListPrice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "xai":{"id":"xai","models":{
	    "grok-4.7":{"id":"grok-4.7","cost":{"input":2,"output":6,"cache_read":0.5}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	grok := Provider{ID: "grok", Name: "Grok (SuperGrok)"}
	for _, id := range []string{"grok-4.7@131072", "x-ai/grok-4.7@131072"} {
		if pr, ok := grok.ListPrice(id); !ok || pr.Input != 2 || pr.Output != 6 || pr.CacheRead != 0.5 {
			t.Errorf("ListPrice(%q) = %+v, %v; want grok-4.7's $2/$6/$0.5", id, pr, ok)
		}
		if pr, ok := MakerPrice(id); !ok || pr.Input != 2 {
			t.Errorf("MakerPrice(%q) = %+v, %v; want grok-4.7's", id, pr, ok)
		}
	}
}

// A Factory account's Fast Mode ids are priced at their base model's list
// price times its maker's fast multiple (factoryFastTimes), cache and
// tiers included, whether the built-in or the plugin serves them. Factory
// answers claude-opus-5-5-fast naming claude-opus-5-5, so with no price
// of its own the call was counted at standard Opus (plugins#119, psylch).
func TestFactoryFastPrice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "anthropic":{"id":"anthropic","models":{
	    "claude-opus-5-5":{"id":"claude-opus-5-5","cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5}},
	    "claude-opus-5":{"id":"claude-opus-5","cost":{"input":5,"output":25,"cache_read":0.5,"cache_write":6.25}}}},
	  "openai":{"id":"openai","models":{
	    "gpt-5.5":{"id":"gpt-5.5","cost":{"input":5,"output":30,"cache_read":0.5,
	      "tiers":[{"tier":{"type":"context","size":272000},"input":10,"output":45,"cache_read":1}]}},
	    "gpt-5.4":{"id":"gpt-5.4","cost":{"input":2.5,"output":15,"cache_read":0.25}},
	    "grok-4.7":{"id":"grok-4.7","cost":{"input":2,"output":6}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	builtin := Provider{ID: "factory", Name: "Factory", Account: &Account{Agent: "factory"}}
	moved := Provider{ID: "factory", Name: "Factory", Account: &Account{plugin: &plugin.Provider{ID: "factory"}}}
	for _, p := range []Provider{builtin, moved} {
		who := p.PluginProvider() + p.Account.Agent
		pr, ok := p.ListPrice("claude-opus-5-5-fast")
		if !ok || pr.Input != 8 || pr.Output != 40 || pr.CacheRead != 0.4 || pr.CacheWrite != 10 || pr.CacheWrite1h != 16 {
			t.Errorf("%s ListPrice(claude-opus-5-5-fast) = %+v, %v; want 2x Opus 5.5: $8/$40, $0.4 read, $10 and $16 writes", who, pr, ok)
		}
		if pr, ok := p.ListPrice("claude-opus-5-fast"); !ok || pr.Input != 10 || pr.Output != 50 {
			t.Errorf("%s ListPrice(claude-opus-5-fast) = %+v, %v; want $10/$50", who, pr, ok)
		}
		pr, ok = p.ListPrice("gpt-5.5-fast")
		if !ok || pr.Input != 12.5 || pr.Output != 75 || pr.CacheRead != 1.25 {
			t.Errorf("%s ListPrice(gpt-5.5-fast) = %+v, %v; want 2.5x: $12.5/$75/$1.25", who, pr, ok)
		} else if long := pr.At(300000); long.Input != 25 || long.Output != 112.5 {
			t.Errorf("%s gpt-5.5-fast past 272k = %+v; want 2.5x its tier: $25/$112.5", who, long)
		}
		if pr, ok := p.ListPrice("gpt-5.4-fast"); !ok || pr.Input != 5 || pr.Output != 30 || pr.CacheRead != 0.5 {
			t.Errorf("%s ListPrice(gpt-5.4-fast) = %+v, %v; want $5/$30/$0.5", who, pr, ok)
		}
		// the base model stays at its own price
		if pr, ok := p.ListPrice("claude-opus-5-5"); !ok || pr.Input != 4 {
			t.Errorf("%s ListPrice(claude-opus-5-5) = %+v, %v; want $4", who, pr, ok)
		}
		// a -fast id Factory has no Fast Mode of
		if pr, ok := p.ListPrice("grok-4.7-fast"); ok {
			t.Errorf("%s ListPrice(grok-4.7-fast) = %+v; want unpriced", who, pr)
		}
	}
	// elsewhere a -fast id is no Factory Fast Mode model
	other := Provider{ID: "claude", Name: "Claude", Account: &Account{Agent: "claude"}}
	if pr, ok := other.ListPrice("claude-opus-5-5-fast"); ok {
		t.Errorf("claude ListPrice(claude-opus-5-5-fast) = %+v; want unpriced", pr)
	}
}

// A Grok account's fast variant, grok-4.7-build-fast, is priced at twice
// grok-4.7, every tier with it, as xAI sells it (https://x.ai/news/grok-4-7:
// "a fast variant with twice the output speed at twice the price"),
// whether the built-in or the Grok plugin serves it: a routing group of
// Cursor's and Grok's Grok 4.7 fast counted no cost for Grok's calls
// (TradingLife on Discord). Elsewhere the id stays unpriced.
func TestGrokBuildFastPrice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	// as models.dev's xai lists it (2026-10-15)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "xai":{"id":"xai","models":{
	    "grok-4.7":{"id":"grok-4.7","cost":{"input":2,"output":6,"cache_read":0.5,
	      "tiers":[{"input":4,"output":12,"cache_read":1,"tier":{"type":"context","size":200000}}]}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	builtin := Provider{ID: "grok", Name: "Grok (SuperGrok)", Account: &Account{Agent: "grok"}}
	moved := Provider{ID: "grok", Name: "Grok (SuperGrok)", Account: &Account{plugin: &plugin.Provider{ID: "grok"}}}
	for _, p := range []Provider{builtin, moved} {
		for _, id := range []string{"grok-4.7-build-fast", "GROK-4.7-Build-Fast", "grok-4.7-build-fast@500k"} {
			pr, ok := p.ListPrice(id)
			if !ok || pr.Input != 4 || pr.Output != 12 || pr.CacheRead != 1 {
				t.Errorf("%q ListPrice(%q) = %+v, %v; want twice grok-4.7's, $4/$12/$1", p.PluginProvider(), id, pr, ok)
				continue
			}
			if long := pr.At(300000); long.Input != 8 || long.Output != 24 || long.CacheRead != 2 {
				t.Errorf("%q ListPrice(%q) past 200k = %+v; want $8/$24/$2", p.PluginProvider(), id, long)
			}
		}
		// the model itself keeps its own price
		if pr, ok := p.ListPrice("grok-4.7"); !ok || pr.Input != 2 {
			t.Errorf("ListPrice(grok-4.7) = %+v, %v; want $2", pr, ok)
		}
		// a fast variant of a model no catalog prices stays unpriced
		if pr, ok := p.ListPrice("grok-4.6-build-fast"); ok {
			t.Errorf("ListPrice(grok-4.6-build-fast) = %+v; want unpriced", pr)
		}
	}
	// not a Grok account: not Grok Build's fast variant
	for _, p := range []Provider{{ID: "grok"}, {ID: "cursor", Account: &Account{Agent: "cursor"}}} {
		if pr, ok := p.ListPrice("grok-4.7-build-fast"); ok {
			t.Errorf("%s ListPrice(grok-4.7-build-fast) = %+v; want unpriced", p.ID, pr)
		}
	}
}
