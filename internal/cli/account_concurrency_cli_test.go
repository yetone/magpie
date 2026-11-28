package cli

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// magpie provider account-concurrency gives a key its own limit on
// requests at once (by its name or id), off for none, default for the
// provider's again; magpie provider queue bounds the queue past it. A
// key the provider hasn't, or a number out of range, is an error.
func TestAccountConcurrencyCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DSH_HOME", "")
	five := 5
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", Chat: "http://127.0.0.1:1/v1", MaxConcurrency: &five}); err != nil {
		t.Fatal(err)
	}
	limit := func() int {
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		return p.LaneLimit()
	}
	if n := limit(); n != 5 {
		t.Fatalf("before: %d, want the provider's 5", n)
	}
	id := provider.KeyID("k1")
	if err := providerCmd([]string{"provider", "account-concurrency", "relay", id, "2"}); err != nil {
		t.Fatal(err)
	}
	if n := limit(); n != 2 {
		t.Fatalf("own: %d, want 2", n)
	}
	for _, args := range [][]string{{"provider", "account-concurrency", "relay"}, {"provider", "account-concurrency", "relay", id}} {
		if err := providerCmd(args); err != nil {
			t.Fatal(err)
		}
	}
	if err := providerCmd([]string{"provider", "account-concurrency", "relay", id, "off"}); err != nil {
		t.Fatal(err)
	}
	if n := limit(); n != 0 {
		t.Fatalf("off: %d, want none", n)
	}
	for _, bad := range []string{"-1", "1001", "lots"} {
		if err := providerCmd([]string{"provider", "account-concurrency", "relay", id, bad}); err == nil {
			t.Fatalf("%s taken", bad)
		}
	}
	if err := providerCmd([]string{"provider", "account-concurrency", "relay", "nokey", "3"}); err == nil {
		t.Fatal("a key it hasn't was limited")
	}
	if err := providerCmd([]string{"provider", "account-concurrency", "relay", id, "default"}); err != nil {
		t.Fatal(err)
	}
	if n := limit(); n != 5 {
		t.Fatalf("default: %d, want the provider's 5", n)
	}

	if err := providerCmd([]string{"provider", "queue", "relay", "4", "30"}); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("relay")
	if p.QueueLimit != 4 || p.QueueWait != 30 {
		t.Fatalf("queue %d, wait %d", p.QueueLimit, p.QueueWait)
	}
	if err := providerCmd([]string{"provider", "queue", "relay", "off"}); err != nil {
		t.Fatal(err)
	}
	if p, _ = provider.Find("relay"); p.QueueLimit != 0 || p.QueueWait != 30 {
		t.Fatalf("off: queue %d, wait %d", p.QueueLimit, p.QueueWait)
	}
	for _, bad := range [][]string{{"-3"}, {"20000"}, {"2", "4000"}, {"x"}} {
		if err := providerCmd(append([]string{"provider", "queue", "relay"}, bad...)); err == nil {
			t.Fatalf("%v taken", bad)
		}
	}
}

// magpie provider rpm sets how many requests each key sends in a minute
// (coeo91 on Discord), off for none; a number out of range is an error.
func TestRPMCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DSH_HOME", "")
	if err := provider.Save(provider.Provider{ID: "or", Name: "OpenRouter", Key: "k1", Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	rpm := func() int {
		p, err := provider.Find("or")
		if err != nil {
			t.Fatal(err)
		}
		return p.RPMLimit()
	}
	if err := providerCmd([]string{"provider", "rpm", "or", "20"}); err != nil {
		t.Fatal(err)
	}
	if n := rpm(); n != 20 {
		t.Fatalf("set: %d, want 20", n)
	}
	if err := providerCmd([]string{"provider", "rpm", "or"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{{"-3"}, {"20000"}, {"x"}, {"2", "3"}} {
		if err := providerCmd(append([]string{"provider", "rpm", "or"}, bad...)); err == nil {
			t.Fatalf("%v taken", bad)
		}
	}
	if n := rpm(); n != 20 {
		t.Fatalf("after refused ones: %d, want 20", n)
	}
	if err := providerCmd([]string{"provider", "rpm", "or", "off"}); err != nil {
		t.Fatal(err)
	}
	if n := rpm(); n != 0 {
		t.Fatalf("off: %d", n)
	}
}

// magpie provider at-once sets how many of a provider's keys take requests
// at the same time (01huadalang on Discord, ZCode), off for no limit and
// default for the plugin's; a number out of range is an error.
func TestAtOnceCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DSH_HOME", "")
	if err := provider.Save(provider.Provider{ID: "zc", Name: "ZC", Key: "k1", Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	set := func() *int {
		p, err := provider.Find("zc")
		if err != nil {
			t.Fatal(err)
		}
		return p.MaxAccounts
	}
	if err := providerCmd([]string{"provider", "at-once", "zc", "2"}); err != nil {
		t.Fatal(err)
	}
	if n := set(); n == nil || *n != 2 {
		t.Fatalf("set: %v, want 2", n)
	}
	if err := providerCmd([]string{"provider", "at-once", "zc"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{{"-1"}, {"20000"}, {"x"}, {"2", "3"}} {
		if err := providerCmd(append([]string{"provider", "at-once", "zc"}, bad...)); err == nil {
			t.Fatalf("%v taken", bad)
		}
	}
	if n := set(); n == nil || *n != 2 {
		t.Fatalf("after refused ones: %v, want 2", n)
	}
	if err := providerCmd([]string{"provider", "at-once", "zc", "off"}); err != nil {
		t.Fatal(err)
	}
	if n := set(); n == nil || *n != 0 {
		t.Fatalf("off: %v, want 0 kept as the user's own", n)
	}
	if err := providerCmd([]string{"provider", "at-once", "zc", "default"}); err != nil {
		t.Fatal(err)
	}
	if n := set(); n != nil {
		t.Fatalf("default: %v, want none of the user's own", *n)
	}
}

// magpie provider first-token sets how long a streamed reply may take to
// begin before the next key is asked (GoForceX on X), off for none of its
// own; a number out of range is an error and keeps the saved one.
func TestFirstTokenCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DSH_HOME", "")
	if err := provider.Save(provider.Provider{ID: "or", Name: "OpenRouter", Key: "k1", Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	ft := func() int {
		p, err := provider.Find("or")
		if err != nil {
			t.Fatal(err)
		}
		return p.FirstToken
	}
	if err := providerCmd([]string{"provider", "first-token", "or", "20"}); err != nil {
		t.Fatal(err)
	}
	if n := ft(); n != 20 {
		t.Fatalf("set: %d, want 20", n)
	}
	if err := providerCmd([]string{"provider", "first-token", "or"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{{"-3"}, {"601"}, {"x"}, {"2", "3"}} {
		if err := providerCmd(append([]string{"provider", "first-token", "or"}, bad...)); err == nil {
			t.Fatalf("%v taken", bad)
		}
	}
	if n := ft(); n != 20 {
		t.Fatalf("after refused ones: %d, want 20", n)
	}
	if err := providerCmd([]string{"provider", "first-token", "or", "off"}); err != nil {
		t.Fatal(err)
	}
	if n := ft(); n != 0 {
		t.Fatalf("off: %d", n)
	}
}
