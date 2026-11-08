package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// outputHome is syncHome with a model models.dev says replies 384000
// tokens, as it says of deepseek-v4.1-flash, served by the relay.
func outputHome(t *testing.T) string {
	t.Helper()
	home := syncHome(t)
	listOutput(t, 384000)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-v4.1-flash"}}); err != nil {
		t.Fatal(err)
	}
	return home
}

// listOutput is models.dev saying the model replies n tokens.
func listOutput(t *testing.T, n int) {
	t.Helper()
	os.WriteFile(catalog.CachePath(), []byte(`{"deepseek":{"models":{"deepseek-v4.1-flash":{"id":"deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash","limit":{"context":1000000,"output":`+strconv.Itoa(n)+`}}}}}`), 0o644)
	catalog.Reset()
}

// sayagoodbyeplz on Discord: WorkBuddy's deepseek-v4.1-flash replies
// 128000 tokens, not the 384000 models.dev says, and a maxTokens set to
// 128000 by hand in Pi's models.json was written back to 384000 by the
// next sync. One typed there stays; a Max output set in magpie is written
// over it; and with neither, magpie's own follows models.dev.
func TestPiKeepsAMaxTokensTypedByHand(t *testing.T) {
	home := outputHome(t)
	piModels := filepath.Join(home, ".pi", "agent", "models.json")
	writeFile(t, filepath.Join(home, ".pi", "agent", "settings.json"), `{"defaultProvider":"magpie","defaultModel":"relay/deepseek-v4.1-flash"}`)
	writeFile(t, piModels, `{"providers":{"magpie":{"name":"magpie","models":[]}}}`)
	setUpInMagpie("pi")
	const at = "providers.magpie.models.0.maxTokens"
	got := func() string { v, _ := edit.GetJSON(piModels, at); return v }
	SyncCatalog()
	if got() != "384000" {
		t.Fatalf("magpie's maxTokens = %s, want models.dev's 384000\n%s", got(), readFile(piModels))
	}

	// models.dev changes, nothing typed: magpie's follows it
	listOutput(t, 300000)
	SyncCatalog()
	if got() != "300000" {
		t.Fatalf("after models.dev changed, maxTokens = %s, want 300000", got())
	}

	// typed by hand: it stays through syncs, and models.dev changing
	if err := edit.SetJSON(piModels, edit.KV{Path: at, Value: 128000}); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	if got() != "128000" {
		t.Fatalf("a maxTokens typed by hand was written over: %s", got())
	}
	listOutput(t, 384000)
	SyncCatalog()
	if got() != "128000" {
		t.Fatalf("a maxTokens typed by hand was written over when models.dev changed: %s", got())
	}

	// set in magpie: the user's word there is written over it
	if err := provider.SetModelOutput("relay/deepseek-v4.1-flash", 100000); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	if got() != "100000" {
		t.Fatalf("a Max output set in magpie didn't reach Pi: %s", got())
	}
	// and taken away, magpie's own again
	if err := provider.SetModelOutput("relay/deepseek-v4.1-flash", 0); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	if got() != "384000" {
		t.Fatalf("with no Max output set, maxTokens = %s, want 384000", got())
	}
}

// The same for OpenCode's limit.output.
func TestOpenCodeKeepsALimitOutputTypedByHand(t *testing.T) {
	home := outputHome(t)
	cfg := filepath.Join(home, ".config", "opencode", "opencode.json")
	writeFile(t, cfg, `{"model":"magpie/relay/deepseek-v4.1-flash","provider":{"magpie":{"name":"magpie","npm":"@ai-sdk/openai-compatible","options":{"baseURL":"http://127.0.0.1:3425/v1","apiKey":"magpie"},"models":{}}}}`)
	oc := opencode(home, filepath.Join(home, ".config"))
	const at = "provider.magpie.models.relay/deepseek-v4\\.1-flash.limit.output"
	got := func() string { v, _ := edit.GetJSON(cfg, at); return v }
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	if got() != "384000" {
		t.Fatalf("magpie's limit.output = %q, want 384000\n%s", got(), readFile(cfg))
	}
	if err := edit.SetJSON(cfg, edit.KV{Path: at, Value: 128000}); err != nil {
		t.Fatal(err)
	}
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	if got() != "128000" {
		t.Fatalf("a limit.output typed by hand was written over: %s", got())
	}
	if err := provider.SetModelOutput("relay/deepseek-v4.1-flash", 100000); err != nil {
		t.Fatal(err)
	}
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	if got() != "100000" {
		t.Fatalf("a Max output set in magpie didn't reach OpenCode: %s", got())
	}
}
