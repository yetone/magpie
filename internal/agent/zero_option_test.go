package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// jiangguochao666 on X: an agent's model picker says free by a model its
// provider lists at $0 (Option.Zero, provider.PricedFree), as OpenRouter's
// foo:free, and not by the paid foo beside it. A price the user sets
// afterwards is seen at once: the list built for the old settings isn't
// kept.
func TestPickerSaysAZeroPricedModelIsFree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"zfree":{"models":{
		"foo:free":{"id":"foo:free","cost":{"input":0,"output":0}},
		"foo":{"id":"foo","cost":{"input":1,"output":2}}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := provider.Save(provider.Provider{ID: "zf", Name: "ZF", Catalog: "zfree", Key: "k", Chat: "https://zf.example/v1", Models: []string{"foo:free", "foo"}}); err != nil {
		t.Fatal(err)
	}
	of := func() map[string]Option {
		out := map[string]Option{}
		for _, o := range viaMagpie("opencode", "magpie/") {
			out[o.Ref] = o
		}
		return out
	}
	got := of()
	if o, ok := got["zf/foo:free"]; !ok || !o.Zero || o.Free {
		t.Fatalf("the $0 model: %+v %v", o, ok)
	}
	if o, ok := got["zf/foo"]; !ok || o.Zero {
		t.Fatalf("the paid model: %+v %v", o, ok)
	}

	s := settings.Load()
	one, zero := 1.0, 0.0
	s.ModelPrices = map[string]settings.ModelPrice{"zf/foo:free": {Input: &one, Output: &one, CacheRead: &zero, CacheWrite: &zero}}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if o := of()["zf/foo:free"]; o.Zero {
		t.Fatalf("a model the user pays for is still free: %+v", o)
	}
}
