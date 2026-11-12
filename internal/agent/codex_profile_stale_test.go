package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Codex wired by its profile alone (codex -p magpie) keeps offering the
// models of a provider that was deleted: the profile's model was one of
// that provider's, so it no longer counted as magpie's and the sync left
// magpie-models.json as it was (JNDliuliuM on X).
func TestCodexProfileListDropsADeletedProvider(t *testing.T) {
	home := syncHome(t)
	stash(map[string]string{codexScopeKey: "profile"})
	if err := provider.Save(provider.Provider{ID: "added", Name: "Added", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m2", "m3"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "kept", Name: "Kept", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"k1"}}); err != nil {
		t.Fatal(err)
	}
	if err := codex(home).Fields[0].Set("added/m2"); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(home, ".codex", "magpie-models.json")
	if !strings.Contains(readFile(list), "added/m3") {
		t.Fatalf("not listed to begin with:\n%s", readFile(list))
	}
	if err := provider.Delete("added"); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	got := readFile(list)
	if strings.Contains(got, "added/") {
		t.Errorf("the deleted provider's models are still listed:\n%s", got)
	}
	if !strings.Contains(got, "kept/k1") {
		t.Errorf("the list was not written again:\n%s", got)
	}
}
