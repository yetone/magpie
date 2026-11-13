package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// One Pricer prices each call at the rate of the key that sent it
// (@tnt12231 on X): a relay's two keys at 0.5× and 1.5× count the same
// model apart within one page, a key without a rate of its own and a call
// whose key wasn't recorded at the provider's 0.8, and the memo never
// hands one key's price to another's call.
func TestPricerKeyPriceRates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	// sol at $2 a million in
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"anthropic":{"id":"anthropic","models":{"sol":{"id":"sol","cost":{"input":2,"output":8}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	half, premium := provider.KeyID("sk-half"), provider.KeyID("sk-premium")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk-half", Chat: "https://relay.example/v1", PriceRate: 0.8,
		Keys:          []provider.KeyAccount{{Name: "Premium", Key: "sk-premium"}, {Name: "Plain", Key: "sk-plain"}},
		KeyPriceRates: map[string]float64{half: 0.5, premium: 1.5}}); err != nil {
		t.Fatal(err)
	}
	price := Pricer()
	now := time.Now()
	for _, c := range []struct {
		key  string
		want float64
	}{{half, 1}, {premium, 3}, {provider.KeyID("sk-plain"), 1.6}, {"", 1.6}, {half, 1}} {
		pr := price(Record{Time: now, Agent: "codex", Provider: "relay", ProviderKeyID: c.key, Model: "sol", Input: 1e6, Status: 200})
		if pr == nil {
			t.Fatalf("key %q: no price", c.key)
		}
		if got := pr.Cost(1e6, 0, 0, 0); got != c.want {
			t.Errorf("key %q: $%v, want $%v", c.key, got, c.want)
		}
	}
}
