package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// magpie provider key-rate <id> <key> factor F sets what the key's rate
// read from its relay is multiplied by (@YIXIE233 on X), and its line
// says it; default takes it off. A bad factor, a factor with no value
// and --help are refused with nothing written.
func TestKeyRateFactorCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DSH_HOME", "")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", KeyName: "Team", Chat: "http://127.0.0.1:1/v1", TopUpRate: 0.07,
		Keys: []provider.KeyAccount{{Name: "Spare", Key: "k2"}}}); err != nil {
		t.Fatal(err)
	}
	// the group rate as the relay was last read, kept beside providers.json
	read := `{"relay ` + provider.KeyID("k1") + `":{"rate":2,"known":true,"at":"` + time.Now().UTC().Format(time.RFC3339) + `"}}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "key-rates.json"), []byte(read), 0o600); err != nil {
		t.Fatal(err)
	}
	rateOf := func() float64 {
		t.Helper()
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		r, _ := p.KeyPriceRate(provider.KeyID("k1"))
		return r
	}
	if r := rateOf(); r != 0.14 {
		t.Fatalf("read: %v", r)
	}
	out, err := stdoutOf(t, func() error { return providerCmd([]string{"provider", "key-rate", "relay", "Team", "factor", "1.5"}) })
	if err != nil {
		t.Fatal(err)
	}
	if r := rateOf(); r != 0.21 {
		t.Fatalf("factor 1.5: %v", r)
	}
	if !strings.Contains(out, "×0.21, its group's ×2 read from the relay, at the top-up rate ×0.07 × factor 1.5") {
		t.Fatalf("line: %q", out)
	}
	for _, bad := range [][]string{{"relay", "Team", "factor"}, {"relay", "Team", "factor", "0"}, {"relay", "Team", "factor", "--help"}, {"relay", "Team", "factor", "1.23456"}, {"relay", "nokey", "factor", "2"}, {"relay", "Team", "1", "2"}} {
		if err := providerCmd(append([]string{"provider", "key-rate"}, bad...)); err == nil {
			t.Fatalf("%v taken", bad)
		}
	}
	if r := rateOf(); r != 0.21 {
		t.Fatalf("a refused value changed it: %v", r)
	}
	out, err = stdoutOf(t, func() error {
		return providerCmd([]string{"provider", "key-rate", "relay", "Team", "factor", "default"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := rateOf(); r != 0.14 || strings.Contains(out, "factor") {
		t.Fatalf("default: %v %q", r, out)
	}
}
