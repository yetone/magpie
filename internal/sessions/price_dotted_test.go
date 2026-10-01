package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A session's model is priced at its maker's list price when its id spells
// the version with a dot (Copilot's claude-opus-4.6) and models.dev's maker
// entry with a dash; one models.dev no longer prices takes the price the user
// gave it from any provider, and is otherwise unpriced (tony on Discord: old
// sessions' copilot/claude models had no known price).
func TestPriceOfDottedAndAnyProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"anthropic":{"id":"anthropic","models":{
	    "claude-opus-4-6":{"id":"claude-opus-4-6","cost":{"input":5,"output":25,"cache_read":0.5,"cache_write":6.25}},
	    "claude-haiku-4-5":{"id":"claude-haiku-4-5","cost":{"input":1,"output":5,"cache_read":0.1,"cache_write":1.25}}}},
	  "openai":{"id":"openai","models":{"gpt-5.4":{"id":"gpt-5.4","cost":{"input":2.5,"output":15}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	for m, in := range map[string]float64{
		"claude-opus-4.6": 5, "claude-haiku-4.5": 1, "Claude-Opus-4.6": 5,
		"gone/claude-opus-4.6": 5, "gpt-5.4": 2.5,
	} {
		if pr, ok := priceOf(settings.Load(), m); !ok || pr.Input != in {
			t.Errorf("priceOf(%q) = %+v, %v; want $%v in", m, pr, ok, in)
		}
	}
	if pr, ok := priceOf(settings.Load(), "gemini-3-pro-preview"); ok {
		t.Errorf("gemini-3-pro-preview priced at %+v with nothing listing it", pr)
	}

	if err := provider.SetModelPrice("*/gemini-3-pro-preview", &catalog.Price{Input: 2, Output: 12}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"gemini-3-pro-preview", "gone/gemini-3-pro-preview"} {
		if pr, ok := priceOf(settings.Load(), m); !ok || pr.Input != 2 || pr.Output != 12 {
			t.Errorf("priceOf(%q) = %+v, %v; want the user's $2/$12", m, pr, ok)
		}
	}
}
