package gui

import (
	"os"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A model disabled in Settings → Models (#1867) is still in its provider's
// editor, told by the pattern that turns it off, so its chip can say so
// instead of reading as a pick the gateway then refuses.
func TestProviderInfoSaysADisabledModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:1/v1", Key: "key", Models: []string{"claude-fable-5", "exact-one", "vendor/tail", "kept"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetDisabledModels([]string{"*FABLE*", "relay/exact-one", "tail"}); err != nil {
		t.Fatal(err)
	}
	got, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"claude-fable-5": "*FABLE*", "exact-one": "relay/exact-one", "vendor/tail": "tail", "kept": ""}
	seen := map[string]bool{}
	for _, m := range providerInfo(*got, nil).Models {
		seen[m.ID] = true
		if w, ok := want[m.ID]; ok && m.Disabled != w {
			t.Errorf("%s: disabled %q, want %q", m.ID, m.Disabled, w)
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("%s left out of the editor", id)
		}
	}
}
