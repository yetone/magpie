package cli

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// magpie provider key-balance-token gives a key its own account's access
// token and user id for its balance (Kylin_Cloud on X), changes the id
// alone with -, and takes them off with off; a flag, a user id that isn't
// one or a key the provider hasn't is refused without writing anything.
func TestKeyBalanceTokenCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DSH_HOME", "")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", KeyName: "Mine", Chat: "http://127.0.0.1:1/v1",
		Keys:       []provider.KeyAccount{{Name: "Team", Key: "k2"}},
		BalanceURL: "http://127.0.0.1:1/api/user/self", BalanceToken: "tok-a"}); err != nil {
		t.Fatal(err)
	}
	own := func() provider.KeyBalanceAuth {
		t.Helper()
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		return p.KeyBalanceAuth[provider.KeyID("k2")]
	}
	if err := providerCmd([]string{"provider", "key-balance-token", "relay", "Team", "tok-b", "2"}); err != nil {
		t.Fatal(err)
	}
	if a := own(); a != (provider.KeyBalanceAuth{Token: "tok-b", User: "2"}) {
		t.Fatalf("set: %+v", a)
	}
	if err := providerCmd([]string{"provider", "key-balance-token", "relay", "Team", "-", "7"}); err != nil {
		t.Fatal(err)
	}
	if a := own(); a != (provider.KeyBalanceAuth{Token: "tok-b", User: "7"}) {
		t.Fatalf("user id alone: %+v", a)
	}
	for _, bad := range [][]string{{"--help"}, {"relay", "Team", "--help"}, {"relay", "--help", "tok"}, {"relay", "Team", "tok", "--user=2"}, {"relay", "Team", "tok", "abc"}, {"relay", "nokey", "tok"}, {"relay", "Team", "off", "2"}, {"relay", "Team", "tok", "2", "extra"}, {"relay", "Mine", "-"}} {
		if err := providerCmd(append([]string{"provider", "key-balance-token"}, bad...)); err == nil {
			t.Fatalf("%v taken", bad)
		}
	}
	if a := own(); a != (provider.KeyBalanceAuth{Token: "tok-b", User: "7"}) {
		t.Fatalf("a refused value changed it: %+v", a)
	}
	if p, _ := provider.Find("relay"); len(p.KeyBalanceAuth) != 1 {
		t.Fatalf("a refused value wrote another key's: %+v", p.KeyBalanceAuth)
	}
	if err := providerCmd([]string{"provider", "key-balance-token", "relay", "Team", "off"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := provider.Find("relay"); p.KeyBalanceAuth != nil {
		t.Fatalf("off left %+v", p.KeyBalanceAuth)
	}
}
