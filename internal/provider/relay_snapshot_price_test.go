package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A relay that serves DeepSeek's V4.1 Flash under the id the maker's
// canonical_model_id gives it, with a snapshot date on it in models.dev
// (deepseek-flash is deepseek/deepseek-v4.1-flash-1017), is priced at
// DeepSeek's list price: the Open Design plugin's deepseek-v4.1-flash showed
// no estimated cost (Winnie7572 on X). The ids are those the plugin lists
// (packages/opendesign models.mjs); testdata is models.dev's entries for them,
// as read 2026-11-13.
func TestRelayDeepSeekSnapshotCanonicalPriced(t *testing.T) {
	claudeHome(t)
	b, err := os.ReadFile("testdata/models_dev_opendesign.json")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), b, 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, c := range []struct {
		model       string
		input, outp float64
	}{
		{"deepseek-v4.1-flash", 0.15, 0.6},
		{"deepseek-v4-flash", 0.15, 0.6},
		{"deepseek-v4-flash-vision-exp", 0.15, 0.6},
		{"deepseek-v4-pro", 0.66, 1.98},
	} {
		pr, ok := EffectivePrice("opendesign", c.model)
		if !ok || pr.Input != c.input || pr.Output != c.outp {
			t.Errorf("opendesign/%s: %+v, %v; want DeepSeek's $%v/$%v", c.model, pr, ok, c.input, c.outp)
		}
	}
	// another vendor's id that only looks alike stays unpriced
	for _, m := range []string{"deepseek-v4.1-flash-0000", "other-v4.1-flash", "deepseek-v4.1-flash-turbo"} {
		if pr, ok := EffectivePrice("opendesign", m); ok {
			t.Errorf("opendesign/%s priced %+v", m, pr)
		}
	}
}
