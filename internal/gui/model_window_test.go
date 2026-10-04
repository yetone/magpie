package gui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// ARNO on Discord (v0.1.815): the window set on a provider's model looked
// not to work, and a model whose id has its vendor's prefix
// (zai/glm-5.3-flash, Cline's cline-free/mimo-v2.6-flash) had none. Agents
// were told both — the catalog's entries take models.dev's window for a
// prefixed id and the user's over it — but the Providers page's model list,
// which the agents' model pickers read too, said only what the vendor's
// list did: nothing, and the same after a window was set. It says the
// window agents are told, and the vendor's own apart (listed).
func TestProviderModelWindowSaid(t *testing.T) {
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
	said := func() map[string][2]int {
		t.Helper()
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][2]int{}
		for _, m := range providerInfo(*p, nil).Models {
			out[m.ID] = [2]int{m.Context, m.Listed}
		}
		served := map[string]int{}
		for _, e := range provider.Served() {
			served[e.Model] = e.Context
		}
		for id, w := range out {
			if served[id] != w[0] {
				t.Errorf("%s: the page says %d, agents are told %d", id, w[0], served[id])
			}
		}
		return out
	}
	if got := said(); got["zai/glm-5.3-flash"] != [2]int{1_000_000, 1_000_000} || got["own-model"] != [2]int{} {
		t.Fatalf("before a window is set: %v", got)
	}
	p, _ := provider.Find("relay")
	if err := provider.SetContext(*p, "zai/glm-5.3-flash", 200_000); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	if err := provider.SetContext(*p, "own-model", 64_000); err != nil {
		t.Fatal(err)
	}
	if got := said(); got["zai/glm-5.3-flash"] != [2]int{200_000, 1_000_000} || got["own-model"] != [2]int{64_000, 0} {
		t.Fatalf("set: %v", got)
	}
}
