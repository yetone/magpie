package cli

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// magpie provider spend-cap gives a key a spend cap in USD (by its name or
// id), a day by default, a month or in all; off takes it off. With no
// amount it lists the keys' caps. A key the provider hasn't, an amount or
// a period it doesn't know, and --help as a value are refused, and
// nothing is written for them.
func TestSpendCapCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DSH_HOME", "")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", KeyName: "Team", Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	capOf := func() (provider.SpendCap, bool) {
		t.Helper()
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		return p.KeySpendCap(provider.KeyID("k1"))
	}
	if err := providerCmd([]string{"provider", "spend-cap", "relay", "Team", "20"}); err != nil {
		t.Fatal(err)
	}
	if c, ok := capOf(); !ok || c.USD != 20 || c.Period != provider.SpendDay {
		t.Fatalf("a day's cap: %+v %v", c, ok)
	}
	if err := providerCmd([]string{"provider", "spend-cap", "relay", provider.KeyID("k1"), "$300", "month"}); err != nil {
		t.Fatal(err)
	}
	if c, ok := capOf(); !ok || c.USD != 300 || c.Period != provider.SpendMonth {
		t.Fatalf("a month's cap: %+v %v", c, ok)
	}
	for _, args := range [][]string{{"relay"}, {"relay", "Team"}} {
		if err := providerCmd(append([]string{"provider", "spend-cap"}, args...)); err != nil {
			t.Fatal(args, err)
		}
	}
	for _, bad := range [][]string{{"relay", "Team", "--help"}, {"relay", "Team", "lots"}, {"relay", "Team", "5", "week"}, {"relay", "Team", "0"}, {"relay", "nokey", "5"}, {"relay", "Team", "5", "day", "extra"}} {
		if err := providerCmd(append([]string{"provider", "spend-cap"}, bad...)); err == nil {
			t.Fatalf("%v taken", bad)
		}
	}
	if c, _ := capOf(); c.USD != 300 || c.Period != provider.SpendMonth {
		t.Fatalf("a refused value changed the cap: %+v", c)
	}
	if err := providerCmd([]string{"provider", "spend-cap", "relay", "Team", "off"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := capOf(); ok {
		t.Fatal("off left the cap")
	}
}
