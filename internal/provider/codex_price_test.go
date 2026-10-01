package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestAutoReviewUsesSharedCatalogPrice(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0700); err != nil {
		t.Fatal(err)
	}
	// Deliberately different prices: the alias must use the catalog, never
	// carry its own rates. Preserve exact-name prices if the catalog adds one.
	write := func(models string) {
		t.Helper()
		if err := os.WriteFile(catalog.CachePath(), []byte(`{"openai":{"id":"openai","models":{`+models+`}}}`), 0600); err != nil {
			t.Fatal(err)
		}
		catalog.Reset()
	}
	t.Cleanup(catalog.Reset)
	write(`"gpt-5.6-luna":{"id":"gpt-5.6-luna","cost":{"input":3,"output":7,"cache_read":1}}`)
	pr, ok := MakerPrice("codex-auto-review")
	if !ok || pr.Input != 3 || pr.Output != 7 || pr.CacheRead != 1 {
		t.Fatalf("alias did not use shared catalog: %+v, %v", pr, ok)
	}
	if PricedName("codex-auto-review") != "gpt-5.6-luna" {
		t.Fatal("missing price reference")
	}
	write(`"codex-auto-review":{"id":"codex-auto-review","cost":{"input":4,"output":8}}`)
	if pr, ok := MakerPrice("codex-auto-review"); !ok || pr.Input != 4 {
		t.Fatalf("exact-name catalog price lost: %+v, %v", pr, ok)
	}
	if PricedName("codex-auto-review") != "codex-auto-review" {
		t.Fatal("price reference must agree with exact-name catalog price")
	}
	write(`"unrelated":{"id":"unrelated","cost":{"input":1,"output":2}}`)
	if _, ok := MakerPrice("codex-auto-review"); ok {
		t.Fatal("missing catalog price must remain unknown")
	}
}
