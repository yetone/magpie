package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
)

// windowCatalog is a catalog with a Claude model priced past 200K as
// models.dev writes it, a GPT model, and a model whose id has an @ in it of
// its own.
const windowCatalog = `{
  "anthropic":{"id":"anthropic","models":{
    "claude-opus-5-5":{"id":"claude-opus-5-5","cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5,
      "tiers":[{"tier":{"type":"context","size":200000},"input":8,"output":30}]}},
    "claude-opus-4-5@20251101":{"id":"claude-opus-4-5@20251101","cost":{"input":5,"output":25}}}},
  "openai":{"id":"openai","models":{
    "gpt-5.5":{"id":"gpt-5.5","cost":{"input":1.25,"output":10,"cache_read":0.125}}}}}`

func windowHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(windowCatalog), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// The Cursor plugin lists a model once per context size Cursor's picker
// has it at (claude-opus-5-5@300k, claude-opus-5-5@1m, gpt-5.5@272k), and
// every call on one was counted at no cost: no catalog lists the sized id,
// and only a Grok id was read as its model at a window (RyuJeXu on X:
// "cursor的用量好像不计费"). Each is its model at that window, at the
// model's price, the long-prompt tier with it; the plain id is priced as
// before, whether the plugin serves the account or the provider is gone.
func TestCursorWindowIDsPricedAsTheirModel(t *testing.T) {
	windowHome(t)
	cursor := Provider{ID: "cursor", Name: "Cursor", Account: &Account{plugin: &plugin.Provider{ID: "cursor"}}}
	for _, c := range []struct {
		id      string
		in, out float64
	}{
		{"claude-opus-5-5", 4, 20},
		{"claude-opus-5-5@300k", 4, 20},
		{"claude-opus-5-5@1m", 4, 20},
		{"Claude-Opus-5-5@1M", 4, 20},
		{"anthropic/claude-opus-5-5@300k", 4, 20},
		{"gpt-5.5@272k", 1.25, 10},
		{"gpt-5.5@1m", 1.25, 10},
	} {
		for name, price := range map[string]func(string) (catalog.Price, bool){"ListPrice": cursor.ListPrice, "MakerPrice": MakerPrice} {
			pr, ok := price(c.id)
			if !ok || pr.Input != c.in || pr.Output != c.out {
				t.Errorf("%s(%q) = %+v, %v; want $%v / $%v", name, c.id, pr, ok, c.in, c.out)
			}
		}
	}
	// the window doesn't set the rate, the prompt does: a 300K prompt at
	// either size is at the model's long tier, a short one at its base
	pr, _ := cursor.ListPrice("claude-opus-5-5@1m")
	if long := pr.At(300_000); long.Input != 8 || long.Output != 30 {
		t.Errorf("claude-opus-5-5@1m at 300K = %+v; want the >200K tier $8 / $30", long)
	}
	if short := pr.At(50_000); short.Input != 4 {
		t.Errorf("claude-opus-5-5@1m at 50K = %+v; want $4", short)
	}
	if got := PricedName("claude-opus-5-5@300k"); got != "claude-opus-5-5" {
		t.Errorf("PricedName(claude-opus-5-5@300k) = %q; want claude-opus-5-5", got)
	}
	if got := PricedNameFor("cursor", "gpt-5.5@272k"); got != "gpt-5.5" {
		t.Errorf("PricedNameFor(cursor, gpt-5.5@272k) = %q; want gpt-5.5", got)
	}
}

// Only a window is read off an id: an @ with a date after it is part of
// the model's own id (a dated id, claude-opus-4-5@20251101, priced as listed),
// an @ with no k or m is no window outside Grok's ids, and a sized id of a
// model no catalog prices stays unpriced.
func TestWindowOnlyReadOffAnID(t *testing.T) {
	windowHome(t)
	if pr, ok := MakerPrice("claude-opus-4-5@20251101"); !ok || pr.Input != 5 {
		t.Errorf("MakerPrice(claude-opus-4-5@20251101) = %+v, %v; want its own $5", pr, ok)
	}
	if got := pricedNames("claude-opus-4-5@20251101"); len(got) != 1 {
		t.Errorf("pricedNames(claude-opus-4-5@20251101) = %q; want the id alone", got)
	}
	cursor := Provider{ID: "cursor", Name: "Cursor", Account: &Account{plugin: &plugin.Provider{ID: "cursor"}}}
	for _, id := range []string{"gpt-5.5@272", "gpt-5.5@", "gpt-5.5@272kb", "muse-wonder-1.0@300k", "auto", "@300k"} {
		if pr, ok := cursor.ListPrice(id); ok {
			t.Errorf("ListPrice(%q) = %+v; want unpriced", id, pr)
		}
	}
}
