package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// Devin's Adaptive and Fusion route between models inside Devin's own
// agent, and its chat API answers either with 502 "no API providers are
// available". The built-in listed them before it served the API
// (ce8d0e065), and a user who picked every model then kept "Adaptive" and
// "adaptive" among the picks it saved: moved onto the plugin, which lists
// neither, they were still served as picks of the user's own, and
// /v1/models offered devin/Adaptive and devin/adaptive, refused each time
// (the owner's providers.json, 2026-11-14). Picks saved so are dropped as
// the file is read, and a save keeps them out.
func TestDevinRouterPicksNotServed(t *testing.T) {
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", "")
	spec := "@magpie-community/opencode-devin-auth"
	write := func(name string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		os.MkdirAll(settings.Dir(), 0o700)
		if err := os.WriteFile(filepath.Join(settings.Dir(), name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("plugins.json", map[string]any{"plugins": []map[string]any{{"spec": spec}}})
	write("plugin-auth.json", map[string]map[string]any{"devin": {"type": "api", "key": "devin-key-1234"}})
	write("migrations.json", map[string]any{"devin": map[string]any{"state": "plugin", "package": spec}})
	// the picks as the owner's providers.json has them, read as written
	write("providers.json", map[string]any{"providers": []map[string]any{{"id": "devin", "name": "", "key": "",
		"models":   []string{"Adaptive", "adaptive", "swe-2", "claude-opus-5-5", "Fusion", "fusion"},
		"delisted": []string{"fusion", "swe-1-6"}}}})
	// the plugin's list (packages/devin listed()): no Adaptive, no Fusion
	plugin.UseCached([]plugin.Provider{{ID: "devin", Spec: spec, Name: "Devin",
		Methods:  []plugin.Method{{Type: "api", Label: "API key"}},
		Accounts: []plugin.Account{{Key: "devin", Type: "api"}},
		Models: listedModels(t, `[
			{"id": "swe-2", "name": "SWE-2"},
			{"id": "claude-opus-5-5", "name": "Claude Opus 5.5"}]`),
	}})
	t.Cleanup(func() { plugin.UseCached(nil) })
	if !Moved("devin") {
		t.Fatal("devin not moved")
	}

	want := []string{"swe-2", "claude-opus-5-5"}
	if got := servedIDs(t, "devin"); !slices.Equal(got, want) {
		t.Fatalf("served %v, want %v", got, want)
	}
	p, err := Find("devin")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Picks(), want) || !slices.Equal(p.Delisted, []string{"swe-1-6"}) {
		t.Fatalf("picks %v, set aside %v", p.Picks(), p.Delisted)
	}
	// a save, of the picks as an editor sends them back, keeps them out
	p.Models = append(p.Models, "adaptive")
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(settings.Dir(), "providers.json"))
	var f struct{ Providers []Provider }
	if err := json.Unmarshal(b, &f); err != nil || len(f.Providers) == 0 {
		t.Fatalf("providers.json: %s (%v)", b, err)
	}
	for _, q := range f.Providers {
		if q.ID == "devin" && (slices.ContainsFunc(q.Models, devinRouter) || slices.ContainsFunc(q.Delisted, devinRouter)) {
			t.Fatalf("saved %v, set aside %v", q.Models, q.Delisted)
		}
	}
	// another provider's model of the name is its own
	if got := normalize(Provider{ID: "other", Models: []string{"adaptive"}}).Models; !slices.Equal(got, []string{"adaptive"}) {
		t.Fatalf("another provider's picks %v", got)
	}
}
