package cli

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// addresses= gives a Remote magpie its other addresses (evjohnicu on X),
// comma or space apart; empty clears them; another provider is told it
// has none to take.
func TestAddressesPair(t *testing.T) {
	p := provider.Provider{ID: "office", Preset: provider.RemoteMagpiePreset}
	if err := applyPairs(&p, []string{"addresses=http://100.101.2.3:3425, 10.144.0.2:3425"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"http://100.101.2.3:3425", "10.144.0.2:3425"}; !slices.Equal(p.Addresses, want) {
		t.Errorf("%q, want %q", p.Addresses, want)
	}
	if err := applyPairs(&p, []string{"addresses="}); err != nil || len(p.Addresses) != 0 {
		t.Errorf("cleared: %q %v", p.Addresses, err)
	}
	r := provider.Provider{ID: "relay", Chat: "https://relay.example.com/v1"}
	if err := applyPairs(&r, []string{"addresses=http://10.0.0.1"}); err == nil {
		t.Error("a relay took other addresses")
	}
}
