package cli

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// magpie provider key-balance sets, shows and takes off a key's balance
// typed by hand (tenacious_eagle on Discord), and a flag or an amount
// that isn't one is refused without writing anything.
func TestKeyBalanceCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DSH_HOME", "")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", KeyName: "Team", Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	typed := func() (provider.OwnBalance, bool) {
		t.Helper()
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		return p.KeyOwnBalance(provider.KeyID("k1"))
	}
	if err := providerCmd([]string{"provider", "key-balance", "relay", "Team", "$1,250.5"}); err != nil {
		t.Fatal(err)
	}
	if b, ok := typed(); !ok || b.USD != 1250.5 || b.At.IsZero() {
		t.Fatalf("typed: %+v %v", b, ok)
	}
	for _, args := range [][]string{{"relay"}, {"relay", "Team"}} {
		if err := providerCmd(append([]string{"provider", "key-balance"}, args...)); err != nil {
			t.Fatal(args, err)
		}
	}
	for _, bad := range [][]string{{"relay", "Team", "--help"}, {"relay", "--help"}, {"relay", "Team", "lots"}, {"relay", "Team", "-3"}, {"relay", "nokey", "5"}, {"relay", "Team", "5", "extra"}} {
		if err := providerCmd(append([]string{"provider", "key-balance"}, bad...)); err == nil {
			t.Fatalf("%v taken", bad)
		}
	}
	if b, _ := typed(); b.USD != 1250.5 {
		t.Fatalf("a refused value changed the balance: %+v", b)
	}
	if err := providerCmd([]string{"provider", "key-balance", "relay", "Team", "off"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := typed(); ok {
		t.Fatal("off left a balance")
	}
}
