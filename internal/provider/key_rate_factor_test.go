package provider

import (
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// @YIXIE233 on X: under one relay, groups whose upstreams cache 60% and
// 90% of a prompt cost 2-3x apart at the same group rate. A key's factor
// multiplies its read rate (group rate x top-up rate), in the cost of a
// call and in routing by price. A key's own rate isn't multiplied, a free
// group stays free, and a key with none bills as before.
func TestKeyRateFactorTimesReadRate(t *testing.T) {
	keyRateHome(t)
	f := &relayKeys{reply: map[string]func(w http.ResponseWriter){"sk-cheap": billing("1"), "sk-pro": billing("2.5"), "sk-own": billing("4"), "sk-free": billing("0")}}
	srv := f.serve(t)
	cheap, pro, own, free := keyID("sk-cheap"), keyID("sk-pro"), keyID("sk-own"), keyID("sk-free")
	if err := Save(Provider{ID: "openai", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-cheap", Models: []string{"sol"}, PriceRate: 0.9, TopUpRate: 0.07,
		Keys:           []KeyAccount{{Name: "Pro", Key: "sk-pro"}, {Name: "Own", Key: "sk-own"}, {Name: "Free", Key: "sk-free"}},
		KeyPriceRates:  map[string]float64{own: 0.3},
		KeyRateFactors: map[string]float64{cheap: 2.5, own: 3, free: 2}}); err != nil {
		t.Fatal(err)
	}
	readKeyRatesDue(t.Context())
	s := settings.Load()
	for _, c := range []struct {
		key  string
		want float64
	}{
		{cheap, 2 * 0.07 * 2.5}, // $2 list x 1 x 0.07 x 2.5
		{pro, 2 * 0.175},        // no factor: x 2.5 x 0.07
		{own, 2 * 0.3},          // the user's own rate, not multiplied
		{free, 0},               // a free group stays free
		{"", 2 * 0.9},           // unknown key: the provider's
	} {
		pr, ok, _ := BaseKeyPriceIn(s, "openai", c.key, "sol")
		if !ok || !near(pr.Input, c.want) {
			t.Errorf("key %q: $%v in, want $%v (%v)", c.key, pr.Input, c.want, ok)
		}
	}
	p, _ := Find("openai")
	if pr, ok := p.PriceAt(s, "sol", time.Time{}); !ok || !near(pr.Input, 0.35) {
		t.Fatalf("PriceAt with Cheap: %+v %v", pr, ok)
	}
	if r, own := p.KeyPriceRate(cheap); own || !near(r, 0.175) {
		t.Fatalf("KeyPriceRate: %v %v", r, own)
	}
	if p.KeyRateFactor(pro) != 1 || p.KeyRateFactor(cheap) != 2.5 {
		t.Fatalf("factors: %v %v", p.KeyRateFactor(pro), p.KeyRateFactor(cheap))
	}

	// set and taken off by the key's name; nothing more is asked of the relay
	asked := f.count()
	g := 0.8
	if err := SetKeyRateFactor("openai", "Pro", &g); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("openai")
	if r, _ := p.KeyPriceRate(pro); !near(r, 0.14) {
		t.Fatalf("Pro at 0.8: %v", r)
	}
	if err := SetKeyRateFactor("openai", cheap, nil); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("openai")
	if r, _ := p.KeyPriceRate(cheap); !near(r, 0.07) {
		t.Fatalf("Cheap with none: %v", r)
	}
	readKeyRatesDue(t.Context())
	if n := f.count(); n != asked {
		t.Fatalf("a factor asked the relay again: %d, was %d", n, asked)
	}
	bad := 0.00001
	if err := SetKeyRateFactor("openai", "Pro", &bad); err == nil {
		t.Fatal("five decimals taken")
	}
	if err := SetKeyRateFactor("openai", "nokey", &g); err == nil {
		t.Fatal("a key it hasn't taken")
	}
}

// A factor is above 0 up to 1000 with four decimals at most; one kept
// wrong in the file, or 1, is dropped on Save, and key ids are kept in
// lower case.
func TestKeyRateFactorOK(t *testing.T) {
	for _, f := range []float64{0.0001, 0.5, 1.25, 3, 1000} {
		if e := KeyRateFactorOK(f); e != "" {
			t.Errorf("%v: %s", f, e)
		}
	}
	for _, f := range []float64{0, -1, 1000.5, 1.23456} {
		if KeyRateFactorOK(f) == "" {
			t.Errorf("%v taken", f)
		}
	}
	priceHome(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-a", TopUpRate: 0.07,
		KeyRateFactors: map[string]float64{"AAAA": 1.5, "bbbb": 1, "cccc": 0, "dddd": 2.00001}}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if len(p.KeyRateFactors) != 1 || p.KeyRateFactors["aaaa"] != 1.5 {
		t.Fatalf("kept %v", p.KeyRateFactors)
	}
	if p.KeyRateFactor("AAAA") != 1.5 || p.KeyRateFactor("dddd") != 1 {
		t.Fatalf("read %v %v", p.KeyRateFactor("AAAA"), p.KeyRateFactor("dddd"))
	}
}

func TestParseKeyRateFactor(t *testing.T) {
	for in, want := range map[string]float64{"1.5": 1.5, "1.5x": 1.5, "×0.8": 0.8, " 2 ": 2} {
		f, err := ParseKeyRateFactor(in)
		if err != nil || f == nil || *f != want {
			t.Errorf("%q: %v %v", in, f, err)
		}
	}
	for _, in := range []string{"default", "off", "none", "-"} {
		if f, err := ParseKeyRateFactor(in); err != nil || f != nil {
			t.Errorf("%q: %v %v", in, f, err)
		}
	}
	for _, in := range []string{"", "--help", "lots", "0", "-1", "1001", "1.00001"} {
		if f, err := ParseKeyRateFactor(in); err == nil {
			t.Errorf("%q taken as %v", in, *f)
		}
	}
}
