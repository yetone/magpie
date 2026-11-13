package provider

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// A key's own price rate (@tnt12231 on X) is set by the key's id or name,
// kept by its fingerprint in lower case, and taken off with nil; a key
// without one bills at the provider's PriceRate. A rate of 0 or out of
// range and a key the provider hasn't are refused, a subscription has no
// keys to rate, and a key's value replaced keeps its rate.
func TestKeyPriceRateSet(t *testing.T) {
	priceHome(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-half", KeyName: "Half", PriceRate: 0.8,
		Keys: []KeyAccount{{Name: "Premium", Key: "sk-premium"}, {Name: "Plain", Key: "sk-plain"}}}); err != nil {
		t.Fatal(err)
	}
	half, premium, plain := keyID("sk-half"), keyID("sk-premium"), keyID("sk-plain")
	rateOf := func(ref string) (float64, bool) {
		t.Helper()
		p, err := Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		return p.KeyPriceRate(ref)
	}
	if r, own := rateOf(half); own || r != 0.8 {
		t.Fatalf("no rate of its own, the provider's: %v %v", r, own)
	}
	if err := SetKeyPriceRate("relay", "Half", new(0.5)); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyPriceRate("relay", strings.ToUpper(premium), new(1.2)); err != nil {
		t.Fatal(err)
	}
	if r, own := rateOf(strings.ToUpper(half)); !own || r != 0.5 {
		t.Fatalf("Half: %v %v", r, own)
	}
	if r, own := rateOf(premium); !own || r != 1.2 {
		t.Fatalf("Premium: %v %v", r, own)
	}
	if r, own := rateOf(plain); own || r != 0.8 {
		t.Fatalf("Plain follows the provider: %v %v", r, own)
	}
	if p, _ := Find("relay"); p.PriceRate != 0.8 {
		t.Fatalf("the provider's own rate changed: %v", p.PriceRate)
	}
	for _, bad := range []float64{0, -1, 1001, 0.1234} {
		if err := SetKeyPriceRate("relay", "Plain", new(bad)); err == nil {
			t.Fatalf("%v was taken", bad)
		}
	}
	if err := SetKeyPriceRate("relay", "nobody", new(1.0)); err == nil {
		t.Fatal("a key the provider hasn't got a rate")
	}
	// the value replaced, the rate goes with the key
	if _, _, err := ReplaceKey("relay", half, "sk-half-2"); err != nil {
		t.Fatal(err)
	}
	if r, own := rateOf(keyID("sk-half-2")); !own || r != 0.5 {
		t.Fatalf("replaced, Half's rate %v %v", r, own)
	}
	if err := SetKeyPriceRate("relay", "Half", nil); err != nil {
		t.Fatal(err)
	}
	if r, own := rateOf(keyID("sk-half-2")); own || r != 0.8 {
		t.Fatalf("taken off, the provider's again: %v %v", r, own)
	}
	// a rate kept wrong in the file (typed by hand) is left out on Save
	p, _ := Find("relay")
	p.KeyPriceRates = map[string]float64{strings.ToUpper(plain): 2, premium: 0, "": 3}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("relay"); len(p.KeyPriceRates) != 1 || p.KeyPriceRates[plain] != 2 {
		t.Fatalf("kept: %v", p.KeyPriceRates)
	}
}

func TestKeyPriceRateOnASubscription(t *testing.T) {
	user := signIn(t)
	if err := SetKeyPriceRate("codex", user, new(0.5)); err == nil {
		t.Fatal("a subscription's account got a key's rate")
	}
}

func TestParseKeyPriceRate(t *testing.T) {
	for in, want := range map[string]float64{"0.8": 0.8, "1.5x": 1.5, "×1.2": 1.2, " 2 ": 2, "X0.5": 0.5} {
		r, err := ParseKeyPriceRate(in)
		if err != nil || r == nil || *r != want {
			t.Errorf("%q: %v %v", in, r, err)
		}
	}
	for _, in := range []string{"default", "Provider", "off", "-"} {
		if r, err := ParseKeyPriceRate(in); err != nil || r != nil {
			t.Errorf("%q: %v %v", in, r, err)
		}
	}
	for _, in := range []string{"0", "abc", "1001", "0.1234", ""} {
		if _, err := ParseKeyPriceRate(in); err == nil {
			t.Errorf("%q taken", in)
		}
	}
}

// Each key's calls are priced at its own rate: two keys of one relay
// count one model's list price apart, a key without a rate and a call
// whose key isn't known at the provider's, and a price the user set for
// the model stands as set for every key.
func TestKeyPriceRatePrices(t *testing.T) {
	priceHome(t)
	priceCatalog(t)
	half, premium := keyID("sk-half"), keyID("sk-premium")
	if err := Save(Provider{ID: "openai", Name: "OpenAI", Chat: "https://api.openai.com/v1", Key: "sk-half", Models: []string{"sol"}, PriceRate: 0.8,
		Keys:          []KeyAccount{{Name: "Premium", Key: "sk-premium"}, {Name: "Plain", Key: "sk-plain"}},
		KeyPriceRates: map[string]float64{half: 0.5, premium: 1.5}}); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	for _, c := range []struct {
		key  string
		want float64
	}{{half, 1}, {premium, 3}, {keyID("sk-plain"), 1.6}, {"", 1.6}, {"0000000000", 1.6}} {
		pr, ok, _ := BaseKeyPriceIn(s, "openai", c.key, "sol")
		if !ok || pr.Input != c.want {
			t.Errorf("key %q: $%v in, want $%v (%v)", c.key, pr.Input, c.want, ok)
		}
	}
	// routing by price weighs a candidate at the rate of the key it holds
	p, _ := Find("openai")
	q := *p
	q.Key = "sk-premium"
	if pr, ok := q.PriceAt(s, "sol", time.Time{}); !ok || pr.Input != 3 {
		t.Fatalf("PriceAt with Premium: %+v %v", pr, ok)
	}
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{"openai/sol": statePrice(7)}}); err != nil {
		t.Fatal(err)
	}
	s = settings.Load()
	for _, k := range []string{half, premium, ""} {
		if pr, ok, _ := BaseKeyPriceIn(s, "openai", k, "sol"); !ok || pr.Input != 7 {
			t.Errorf("key %q: a price set for the model is not scaled: %+v %v", k, pr, ok)
		}
	}
}
