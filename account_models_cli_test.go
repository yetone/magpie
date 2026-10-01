package main

import (
	"os"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// magpie provider account-models gives one key, by its name or its id, the
// models it alone serves; all gives it every one again, and a key the
// provider doesn't have is an error (#474).
func TestAccountModelsCmd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DSH_HOME", "")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", Keys: []provider.KeyAccount{{Name: "Team", Key: "k2"}}, Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	stored := func() map[string][]string {
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		return p.AccountModels
	}
	if err := providerCmd([]string{"provider", "account-models", "relay", "team", "cheap", "mini", "cheap"}); err != nil {
		t.Fatal(err)
	}
	if got, want := stored(), map[string][]string{provider.KeyID("k2"): {"cheap", "mini"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("by name: %v", got)
	}
	if err := providerCmd([]string{"provider", "account-models", "relay", provider.KeyID("k1"), "big"}); err != nil {
		t.Fatal(err)
	}
	if got := stored(); len(got) != 2 || !reflect.DeepEqual(got[provider.KeyID("k1")], []string{"big"}) {
		t.Fatalf("by id: %v", got)
	}
	// a save of the provider's other settings keeps them
	p, _ := provider.Find("relay")
	p.Models = []string{"cheap", "mini", "big"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"team", provider.KeyID("k1")} {
		if err := providerCmd([]string{"provider", "account-models", "relay", ref, "all"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := stored(); got != nil {
		t.Fatalf("all: %v", got)
	}
	if err := providerCmd([]string{"provider", "account-models", "relay", "nobody", "big"}); err == nil {
		t.Fatal("an unknown key was taken")
	}
	if err := providerCmd([]string{"provider", "account-models", "relay"}); err != nil {
		t.Fatal(err)
	}
}
