package cli

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// magpie provider key-rate gives a key a price rate of its own (by its
// name or id, @tnt12231 on X), and default has it bill at the provider's
// again. With no rate it lists the keys' rates. A key the provider
// hasn't, a rate that isn't one and --help as a value are refused, and
// nothing is written for them.
func TestKeyRateCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DSH_HOME", "")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", KeyName: "Team", Chat: "http://127.0.0.1:1/v1", PriceRate: 0.8,
		Keys: []provider.KeyAccount{{Name: "Spare", Key: "k2"}}}); err != nil {
		t.Fatal(err)
	}
	rateOf := func(key string) (float64, bool) {
		t.Helper()
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		return p.KeyPriceRate(provider.KeyID(key))
	}
	if err := providerCmd([]string{"provider", "key-rate", "relay", "Team", "0.5"}); err != nil {
		t.Fatal(err)
	}
	if r, own := rateOf("k1"); !own || r != 0.5 {
		t.Fatalf("Team: %v %v", r, own)
	}
	if err := providerCmd([]string{"provider", "key-rate", "relay", provider.KeyID("k2"), "1.2x"}); err != nil {
		t.Fatal(err)
	}
	if r, own := rateOf("k2"); !own || r != 1.2 {
		t.Fatalf("Spare: %v %v", r, own)
	}
	for _, args := range [][]string{{"relay"}, {"relay", "Team"}} {
		if err := providerCmd(append([]string{"provider", "key-rate"}, args...)); err != nil {
			t.Fatal(args, err)
		}
	}
	for _, bad := range [][]string{{"relay", "Team", "--help"}, {"relay", "Team", "lots"}, {"relay", "Team", "0"}, {"relay", "nokey", "1"}, {"relay", "--help"}, {"relay", "Team", "1", "extra"}, {}} {
		if err := providerCmd(append([]string{"provider", "key-rate"}, bad...)); err == nil {
			t.Fatalf("%v taken", bad)
		}
	}
	if r, _ := rateOf("k1"); r != 0.5 {
		t.Fatalf("a refused value changed the rate: %v", r)
	}
	if err := providerCmd([]string{"provider", "key-rate", "relay", "Team", "default"}); err != nil {
		t.Fatal(err)
	}
	if r, own := rateOf("k1"); own || r != 0.8 {
		t.Fatalf("default, the provider's: %v %v", r, own)
	}
}
