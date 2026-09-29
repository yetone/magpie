package provider

import (
	"slices"
	"testing"
)

// Switched off, a provider is kept as it was, and saving it again keeps it
// off: an account's too, which keeps only the user's settings.
func TestProviderOffKept(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "sk-1", Chat: "https://relay.example.com/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetOff("relay", true); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil || !p.Off || p.Key != "sk-1" || !p.Ready() || p.On() {
		t.Fatalf("off: %+v %v", p, err)
	}
	p.Fallback = []string{"x/y"} // another setting saved leaves it off
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("relay"); !p.Off {
		t.Fatal("saved again, relay is on")
	}
	if off, ok := SwitchedOff("relay/m"); !ok || off.ID != "relay" {
		t.Fatalf("SwitchedOff(relay/m): %v %s", ok, off.ID)
	}
	if off, ok := SwitchedOff("m"); !ok || off.ID != "relay" {
		t.Fatalf("SwitchedOff(m): %v %s", ok, off.ID)
	}
	if slices.ContainsFunc(Catalog(), func(e Entry) bool { return e.Provider.ID == "relay" }) {
		t.Fatal("relay listed while off")
	}
	if err := SetOff("relay", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := SwitchedOff("relay/m"); ok {
		t.Fatal("relay switched off after SetOff false")
	}
	if !slices.ContainsFunc(Catalog(), func(e Entry) bool { return e.ID == "relay/m" }) {
		t.Fatal("relay/m not listed once on again")
	}

	// Kiro with a key is an account whose settings are all magpie keeps
	if err := Save(Provider{ID: "kiro", Key: "ksk_1", Off: true}); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(load().Providers, func(p Provider) bool { return p.ID == "kiro" && p.Off }) {
		t.Fatalf("kiro saved on: %+v", load().Providers)
	}
	if k, err := Find("kiro"); err == nil && !k.Off {
		t.Fatal("kiro found on")
	}
}
