package gui

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// tmr11235 on X: "Test on account…" was in the GUI and the provider
// package, but the row the GUI is sent never carried testPicks, so a
// provider with two accounts or keys on offered no such entry. The row
// says the picks, and says none for a provider with one.
func TestProviderInfoSaysTestPicks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	row := func(p provider.Provider) map[string]any {
		t.Helper()
		b, err := json.Marshal(providerInfo(p, nil))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	two := provider.Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:1/v1", Key: "sk-a", KeyName: "first",
		Keys: []provider.KeyAccount{{Name: "second", Key: "sk-b"}, {Name: "off", Key: "sk-c", Off: true}}}
	picks, _ := row(two)["testPicks"].([]any)
	if len(picks) != 2 {
		t.Fatalf("two keys on: testPicks %v", picks)
	}
	one := provider.Provider{ID: "solo", Name: "Solo", Chat: "http://127.0.0.1:1/v1", Key: "sk-a"}
	if _, ok := row(one)["testPicks"]; ok {
		t.Fatal("one key: testPicks sent")
	}
}
