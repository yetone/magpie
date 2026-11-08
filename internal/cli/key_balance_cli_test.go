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
	// in yuan, refilled on the 1st of each month; then weekly, the
	// amount kept; then refilling no more
	if err := providerCmd([]string{"provider", "key-balance", "relay", "Team", "¥700", "--refill", "monthly:1"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := typed(); b.CNY == nil || *b.CNY != 700 || b.USD != 0 || b.Refill == nil || *b.Refill != (provider.Refill{Every: provider.RefillMonth, Day: 1}) {
		t.Fatalf("¥700 monthly:1: %+v", b)
	}
	if err := providerCmd([]string{"provider", "key-balance", "relay", "Team", "--refill=weekly:fri"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := typed(); b.CNY == nil || *b.CNY != 700 || b.Refill == nil || *b.Refill != (provider.Refill{Every: provider.RefillWeek, Day: 5}) {
		t.Fatalf("weekly:fri: %+v", b)
	}
	for _, bad := range [][]string{{"relay", "Team", "--refill", "yearly"}, {"relay", "--refill", "monthly"}, {"relay", "Team", "off", "--refill", "monthly"}, {"relay", "Team", "--refill"}} {
		if err := providerCmd(append([]string{"provider", "key-balance"}, bad...)); err == nil {
			t.Fatalf("%v taken", bad)
		}
	}
	if b, _ := typed(); b.Refill == nil || b.Refill.Every != provider.RefillWeek {
		t.Fatalf("a refused refill changed it: %+v", b)
	}
	if err := providerCmd([]string{"provider", "key-balance", "relay", "Team", "--refill", "off"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := typed(); b.Refill != nil || b.CNY == nil {
		t.Fatalf("refill off: %+v", b)
	}
	// credit that expires (tenacious_eagle_96548): set beside the rest,
	// kept when the balance is typed again in its currency, taken off
	if err := providerCmd([]string{"provider", "key-balance", "relay", "Team", "--expiring", "200@2099-12-31"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := typed(); b.Expiring == nil || b.Expiring.Amount != 200 || b.Expiring.Until != "2099-12-31" || b.Expiring.At.IsZero() || *b.CNY != 700 {
		t.Fatalf("--expiring 200@2099-12-31: %+v", b)
	}
	for _, bad := range [][]string{{"relay", "Team", "--expiring", "200"}, {"relay", "Team", "--expiring", "200@soon"}, {"relay", "Team", "--expiring", "¥200@2099-12-31"},
		{"relay", "Team", "--expiring", "0@2099-12-31"}, {"relay", "--expiring", "1@2099-12-31"}, {"relay", "Team", "off", "--expiring", "1@2099-12-31"}} {
		if err := providerCmd(append([]string{"provider", "key-balance"}, bad...)); err == nil {
			t.Fatalf("%v taken", bad)
		}
	}
	if err := providerCmd([]string{"provider", "key-balance", "relay", "Team", "¥650"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := typed(); b.Expiring == nil || b.Expiring.Amount != 200 {
		t.Fatalf("typed again in yuan, the part that expires went: %+v", b)
	}
	if err := providerCmd([]string{"provider", "key-balance", "relay", "Team", "--expiring=off"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := typed(); b.Expiring != nil || *b.CNY != 650 {
		t.Fatalf("--expiring off: %+v", b)
	}
	if err := providerCmd([]string{"provider", "key-balance", "relay", "Team", "off"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := typed(); ok {
		t.Fatal("off left a balance")
	}
}
