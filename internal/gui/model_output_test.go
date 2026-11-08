package gui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// ARNO on Discord: a model's output limit wasn't in the Gateway's model
// tooltip, though agents were told one when it was written to their
// config. The page's models carried no output at all, so the tooltip had
// a line only for routing groups. Each model now says the reply limit
// agents are told: the vendor's list's, else models.dev's, else the
// user's Max output over both.
func TestProviderModelOutputSaid(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "zai":{"id":"zai","models":{"glm-5.3-flash":{"id":"glm-5.3-flash","limit":{"context":1000000,"output":131072}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"zai/glm-5.3-flash", "own-model"}}); err != nil {
		t.Fatal(err)
	}
	said := func() map[string]int {
		t.Helper()
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for _, m := range providerInfo(*p, nil).Models {
			out[m.ID] = m.Output
		}
		for _, e := range provider.Served() {
			if n, ok := out[e.Model]; ok && n != e.Output {
				t.Errorf("%s: the page says %d, agents are told %d", e.Model, n, e.Output)
			}
		}
		return out
	}
	if got := said(); got["zai/glm-5.3-flash"] != 131072 || got["own-model"] != 0 {
		t.Fatalf("models.dev's: %v", got)
	}
	if err := provider.SetModelOutput("relay/own-model", 64_000); err != nil {
		t.Fatal(err)
	}
	if got := said(); got["own-model"] != 64_000 {
		t.Fatalf("the user's: %v", got)
	}
	// Model settings' Max output shows what is listed when none is set, the
	// user's own aside (sayagoodbyeplz on Discord: 384000 from models.dev
	// beside the 128000 typed over it)
	if err := provider.SetModelOutput("relay/zai/glm-5.3-flash", 100_000); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range providerInfo(*p, nil).Models {
		if m.ID == "zai/glm-5.3-flash" && (m.Output != 100_000 || m.ListedOutput != 131072) {
			t.Errorf("with the user's 100000: output %d, listed %d; want 100000, models.dev's 131072", m.Output, m.ListedOutput)
		}
	}
}

// #1438 (yhong91): a model's reply limit is shown within the window shown
// beside it, as agents are told it and /v1/models gives it — models.dev's
// 500000 for grok-4.7 against the 256000 its backend lists. One within its
// window, or with no window known, is shown as it was, and so is a group's.
func TestProviderModelOutputWithinTheWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"xai":{"id":"xai","models":{"grok-4.7":{"id":"grok-4.7","limit":{"context":500000,"output":500000}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := provider.Save(provider.Provider{ID: "gx", Name: "GX", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"grok-4.7", "own-within", "own-nowindow"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("gx", "http://127.0.0.1:1/v1", []catalog.Model{
		{ID: "grok-4.7", Context: 256000}, {ID: "own-within", Context: 128000, Output: 64000}, {ID: "own-nowindow", Output: 500000}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"gx/grok-4.7"}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("gx")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]int{}
	for _, m := range providerInfo(*p, nil).Models {
		got[m.ID] = [2]int{m.Context, m.Output}
	}
	for id, want := range map[string][2]int{"grok-4.7": {256000, 256000}, "own-within": {128000, 64000}, "own-nowindow": {0, 500000}} {
		if got[id] != want {
			t.Errorf("%s: the page says window, output %v; want %v", id, got[id], want)
		}
	}
	seen := false
	for _, g := range providersState().Gateway.Groups {
		seen = seen || g.ID == provider.GroupPrefix+"g"
		if g.ID == provider.GroupPrefix+"g" && (g.Context != 256000 || g.Output != 256000) {
			t.Errorf("the group: window %d, output %d", g.Context, g.Output)
		}
	}
	if !seen {
		t.Error("the group is not on the Gateway page")
	}
}
