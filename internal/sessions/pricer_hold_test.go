package sessions

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A pass's prices look each model's provider up among all of them, and
// the providers are built once for the pass, not once a model: keptStill
// checks every kept price on each 10 s refresh of the Sessions page, and
// each look built every provider again (~40 MB in 4 minutes).
func TestPricerBuildsTheProvidersOnce(t *testing.T) {
	setup(t)
	PriceOf = priceOf // setup's stub never looks a provider up
	provider.Changed()
	before := provider.AllBuilt.Load()
	price := pricer()
	for _, m := range []string{"openai/gpt-5", "anthropic/claude-opus-5-5", "deepseek/deepseek-chat", "google/gemini-3-pro", "xai/grok-4", "moonshotai/kimi-k2"} {
		price(m)
	}
	if n := provider.AllBuilt.Load() - before; n > 1 {
		t.Fatalf("six models priced built every provider %d times; want once", n)
	}
}
