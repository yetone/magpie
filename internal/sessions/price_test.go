package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A session's Grok model named at an effort is priced as its model; one no
// catalog lists (Grok Build's grok-4.7-build, grok-4.7-mini, a fast one)
// stays unpriced (#224).
func TestPriceOfGrokEffort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"xai":{"id":"xai","models":{
	    "grok-4.7":{"id":"grok-4.7","cost":{"input":2,"output":6,"cache_read":0.5}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, m := range []string{"grok-4.7", "grok-4.7-low", "grok-4.7-xhigh", "x-ai/grok-4.7-high"} {
		if pr, ok := priceOf(m); !ok || pr.Input != 2 || pr.Output != 6 || pr.CacheRead != 0.5 {
			t.Errorf("priceOf(%q) = %+v, %v; want $2/$6/$0.5", m, pr, ok)
		}
	}
	for _, m := range []string{"grok-4.7-build", "grok-4.7-mini", "grok-4.7-low-fast", "grok-4.7-build-fast"} {
		if pr, ok := priceOf(m); ok {
			t.Errorf("priceOf(%q) = %+v; want unpriced", m, pr)
		}
	}
}
