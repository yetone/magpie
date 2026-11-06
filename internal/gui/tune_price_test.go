package gui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/tune"
)

// Tuned for you weighs a model magpie routes at what magpie bills its
// provider, not by its name (#3435). Claude Code is given magpie's models
// as provider/model ids, which no name rule took for Claude: a Claude model
// on an Anthropic-billed provider was weighed with an hour's cache write at
// a 5-minute one's price, so calls seconds apart were left on the hour they
// pay twice for. A model Claude Code named itself keeps the maker's weights.
func TestTuneWeighsRoutedCallsAtTheirProvidersPrice(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	home := t.TempDir()
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	// models.dev's entry as magpie's cache had it on 2026-10-24
	os.WriteFile(catalog.CachePath(), []byte(`{"anthropic":{"id":"anthropic","models":{
	  "claude-opus-5-5":{"id":"claude-opus-5-5","cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: "http://127.0.0.1:1",
		Models: []string{"claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}

	weigh := pricedWeights(settings.Load())
	if w := weigh("relay/claude-opus-5-5"); w != (tune.Weights{Read: 0.05, Write: 1.25, Write1h: 2, Out: 5}) {
		t.Errorf("relay/claude-opus-5-5 weighs %+v; want its price's: read 0.05, write 1.25, hour 2, out 5", w)
	}
	if w := weigh("claude-opus-5-5"); w != tune.WeightsFor("claude-opus-5-5") {
		t.Errorf("Claude Code's own claude-opus-5-5 weighs %+v; want the maker's, %+v", w, tune.WeightsFor("claude-opus-5-5"))
	}

	// calls 20 seconds apart, the cache kept for an hour
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"promptCacheTtl": "1h"}`), 0o644)
	st := tuneOf(tuneCalls("claude", "relay/claude-opus-5-5", 80), 7, home)
	var ttl *tune.TTL
	for _, a := range st.Agents {
		for _, ad := range a.Advice {
			if a.Agent == "claude" && ad.Knob == agent.TuneCache {
				ttl = ad.TTL
			}
		}
	}
	if ttl == nil || ttl.Current != "1h" || ttl.Best != "5m" || ttl.Hour <= ttl.Short {
		t.Fatalf("cache advice for relay/claude-opus-5-5 = %+v; want 1h → 5m, the hour's writes dearer", ttl)
	}
}
