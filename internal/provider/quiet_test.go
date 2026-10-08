package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Quiet is the model magpie has a word on that says no reasoning: its
// vendor's or its maker's list, or its provider's own account list. A model
// nothing speaks for isn't quiet — the gateway sends it the effort the
// agent asked for, as #597 leaves it — and neither is one that thinks.
func TestEntryQuiet(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	os.MkdirAll(filepath.Join(cache, "magpie"), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{
	  "xiaomi": {"models": {
	    "mimo-v2.6-flash": {"id":"mimo-v2.6-flash","reasoning":true,"reasoning_options":[{"type":"toggle"}]}}},
	  "opencode-go": {"models": {
	    "plain": {"id":"plain","reasoning":false},
	    "levelled": {"id":"levelled","reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high"]}]}}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, p := range []Provider{
		// a vendor whose own catalog lists its models, with and without
		// reasoning
		{ID: "vendor", Name: "Vendor", Key: "k", Chat: "http://127.0.0.1:1/v1", Catalog: "opencode-go",
			Models: []string{"plain", "levelled"}},
		// a relay with no catalog of its own: its list, fetched from the
		// vendor, gives ids alone, so nothing says whether it thinks
		{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1",
			Models: []string{"mimo-v2.6-flash", "plain", "levelled", "mystery"}},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	// the relay's own list, as its API answers: ids alone
	if err := catalog.SaveLive("relay", "http://127.0.0.1:1/v1", []catalog.Model{
		{ID: "mimo-v2.6-flash"}, {ID: "plain"}, {ID: "levelled"}, {ID: "mystery"}}); err != nil {
		t.Fatal(err)
	}
	quiet := func(id string, want bool) {
		t.Helper()
		e, ok := ServedEntryOf(id)
		if !ok {
			t.Fatalf("%s not served", id)
		}
		if got := e.Quiet(); got != want {
			t.Errorf("%s: Quiet %v, want %v (reasoning %v, levels %v)", id, got, want, e.Reasoning, e.Efforts)
		}
	}
	// the vendor's own list says: plain doesn't think, levelled does
	quiet("vendor/plain", true)
	quiet("vendor/levelled", false)
	// a relay's list says nothing of reasoning: #597's unknown, sent the
	// effort as the agent asked, rather than treated as one that can't
	// think — "plain" here is a model of another vendor's catalog, and
	// nothing magpie reads speaks for the relay's
	quiet("relay/plain", false)
	quiet("relay/mystery", false)
	// mimo-v2.6-flash thinks with a switch alone (Xiaomi's own list), so
	// it isn't quiet: its effort goes as the agent asked, no more than high
	quiet("relay/mimo-v2.6-flash", false)
	quiet("relay/levelled", false)
	// a provider signed in through a plugin: its own account list is a
	// word on every model it serves, and says nothing of reasoning for
	// this one — the shape the Trae plugin's model was read as (#950)
	plugin := Entry{ID: "plugin/m", Model: "m", Provider: Provider{ID: "plugin",
		Account: &Account{Agent: "plugin", models: func() []catalog.Model { return nil }}}}
	if !plugin.Quiet() {
		t.Errorf("a plugin's model with no reasoning: Quiet false (reasoning %v, levels %v)", plugin.Reasoning, plugin.Efforts)
	}
	thinks := plugin
	thinks.Reasoning = true
	if thinks.Quiet() {
		t.Errorf("a plugin's model that thinks: Quiet true")
	}
	// the user gave the model levels of their own: they say it takes them,
	// so it isn't quiet whatever its vendor's list says (SetModelEfforts,
	// #295) — the gateway fits an effort to them
	if err := SetModelEfforts("relay/mystery", []string{"low", "high"}); err != nil {
		t.Fatal(err)
	}
	quiet("relay/mystery", false)
}
