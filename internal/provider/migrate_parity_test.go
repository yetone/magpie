package provider

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
)

// A built-in moved onto its plugin offers the reasoning levels it did for
// a model its vendor lists none for (Command Code's, Grok's, WorkBuddy's):
// its maker's, as the built-in borrowed them; a plugin of its own gives
// what it says.
func TestMovedKeepsEfforts(t *testing.T) {
	movedPlugin(t, "grok", map[string]map[string]any{})
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"xai":{"id":"xai","models":{"grok-4.7":{"id":"grok-4.7",
	  "reasoning_options":[{"type":"effort","values":["low","medium","high","xhigh"]}]}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	id := "grok-4.7"
	if len(borrowedEfforts(id)) == 0 {
		t.Fatal("the catalog written gives grok-4.7 no efforts")
	}
	pp := plugin.Provider{ID: "grok", Spec: "opencode-grok-auth", Models: []plugin.Model{{ID: id, Reasoning: true}}}
	if got := pluginCatalog(pp)[0].Efforts; !slices.Equal(got, borrowedEfforts(id)) {
		t.Fatalf("moved: %s's efforts %v, want %v", id, got, borrowedEfforts(id))
	}
	pp.ID = "someplugin"
	if got := pluginCatalog(pp)[0].Efforts; len(got) != 0 {
		t.Fatalf("a plugin's own provider borrowed %v", got)
	}
}

// Moved, the agent's own sign-in is the plugin's: the agent signing in to
// another account meanwhile writes no row of the built-in's, which came
// back beside the one set aside as a second own account.
func TestMovedOwnRowNotWritten(t *testing.T) {
	movedPlugin(t, "grok", map[string]map[string]any{})
	sideLogins("grok", "new@x", func(savedLogin) bool { return true })
	for _, l := range readLogins() {
		if l.Agent == "grok" {
			t.Fatalf("a grok row written while moved: %+v", l)
		}
	}
}

// Devin's own account moves with the plan the Devin CLI says, as the
// built-in showed it, though its saved row has none.
func TestDevinOwnMovesWithPlan(t *testing.T) {
	home := claudeHome(t)
	data := filepath.Join(home, "data")
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("APPDATA", data) // where Windows' Devin CLI keeps it
	os.MkdirAll(filepath.Join(data, "devin"), 0o700)
	if err := os.WriteFile(filepath.Join(data, "devin", "credentials.toml"), devinCredentials("cog_own_key_1234", "", "", ""), 0o600); err != nil {
		t.Fatal(err)
	}
	devinStatus.Lock()
	devinStatus.user, devinStatus.plan, devinStatus.ok, devinStatus.at = "dev@example.com", "Devin Pro", true, time.Now()
	devinStatus.Unlock()
	t.Cleanup(forgetDevinStatus)
	own := movingOf(t, "devin")["dev@example.com"]
	md, _ := own.Auth["metadata"].(map[string]any)
	if !own.Own || own.Plan != "Devin Pro" || md["plan"] != "Devin Pro" {
		t.Fatalf("Devin's own: %+v", own)
	}
}
