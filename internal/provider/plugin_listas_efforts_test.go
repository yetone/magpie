package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// axelburks on Discord: a plugin's model that thinks with no variants,
// set the same as and listed as claude-opus-5.5, took the price of Claude
// Opus 5.5 but showed its levels unknown with none ticked. pluginCatalog
// borrowed levels for the plugin's own id, which no catalog knows, and
// the rename to the listed id kept the empty list. The listed id, and
// the model the user said that one is the same as, lend their levels now;
// a model its plugin gives variants keeps them, and the levels the user
// kept of them narrow what is offered.
func TestPluginModelListedAsBorrowsLevels(t *testing.T) {
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", "")
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "anthropic":{"id":"anthropic","models":{"claude-opus-5.5":{"id":"claude-opus-5.5","reasoning":true,
	    "reasoning_options":[{"type":"effort","values":["low","medium","high","xhigh","max"]}],"limit":{"context":1000000},"cost":{"input":5,"output":25}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	spec := "@magpie-community/opencode-qf-auth"
	write := func(name string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		os.MkdirAll(settings.Dir(), 0o700)
		if err := os.WriteFile(filepath.Join(settings.Dir(), name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("plugins.json", map[string]any{"plugins": []map[string]any{{"spec": spec}}})
	write("plugin-auth.json", map[string]map[string]any{"qf": {"type": "oauth", "access": "a", "refresh": "r", "expires": 9999999999999}})
	plugin.UseCached([]plugin.Provider{{ID: "qf", Spec: spec, Name: "QF",
		Methods:  []plugin.Method{{Type: "oauth", Label: "Sign in"}},
		Accounts: []plugin.Account{{Key: "qf", Type: "oauth"}},
		Models: []plugin.Model{
			{ID: "qfmodel", Name: "qfmodel", Reasoning: true},
			{ID: "qfother", Name: "qfother", Reasoning: true},
			{ID: "qfvar", Name: "qfvar", Reasoning: true, Variants: []string{"high"}},
		}}})
	t.Cleanup(func() { plugin.UseCached(nil) })

	str := func(s string) *string { return &s }
	// the reporter's save: the same as and listed as one model at once
	// (the editor's one Save), another listed by a name of the user's own
	// that is the same as it, and one with variants of its own
	if err := SetModelPrefs("qf", map[string]ModelPref{
		"qfmodel": {Same: str("claude-opus-5.5"), ID: str("claude-opus-5.5")},
		"qfother": {Same: str("claude-opus-5.5"), ID: str("my-opus")},
		"qfvar":   {Same: str("claude-opus-5.5"), ID: str("opus-var")},
	}); err != nil {
		t.Fatal(err)
	}
	all := []string{"low", "medium", "high", "xhigh", "max"}
	p, err := Find("qf")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"claude-opus-5.5": all, "my-opus": all, "opus-var": {"high"}}
	got := map[string][]string{}
	for _, m := range p.Available() {
		got[m.ID] = EffortsOf(m) // what the provider's editor shows
	}
	for id, w := range want {
		if g, ok := got[id]; !ok || !slices.Equal(g, w) {
			t.Errorf("%s listed with levels %v (listed: %v), want %v", id, g, ok, w)
		}
		// the gateway fits an effort to the same levels
		if e := p.Efforts(id); !slices.Equal(e, w) {
			t.Errorf("gateway's %s levels %v, want %v", id, e, w)
		}
	}
	// the price came from the same model all along
	if _, ok := EffectivePrice("qf", "claude-opus-5.5"); !ok {
		t.Error("claude-opus-5.5 at qf has no price")
	}

	// the levels the user keeps narrow the borrowed ones
	if err := SetModelEfforts("qf/claude-opus-5.5", []string{"high", "max"}); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("qf")
	var entry *Entry
	for _, e := range Catalog() {
		if e.ID == "qf/claude-opus-5.5" {
			entry = &e
		}
	}
	if entry == nil {
		t.Fatal("no qf/claude-opus-5.5 in the catalog")
	}
	if !slices.Equal(entry.Efforts, []string{"high", "max"}) {
		t.Errorf("kept levels: catalog offers %v, want [high max]", entry.Efforts)
	}
}
