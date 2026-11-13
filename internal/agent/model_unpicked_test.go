package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// JNDliuliuM on X: new models picked for a provider changed Codex's list,
// but Codex CLI, restarted, still started on the old model: its config
// kept the model the user had since unchecked. It goes on to the
// provider's first pick, in each way magpie wires Codex: as its provider,
// beside a ChatGPT sign-in (the CLI and the app read the same config) and
// by its profile alone (codex -p magpie).
func TestCodexLeavesAModelUncheckedInItsProvider(t *testing.T) {
	signedIn := `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`
	for _, c := range []struct {
		name, auth, scope string
		file              string
	}{
		{"as its provider", "", "", "config.toml"},
		{"beside the sign-in", signedIn, "", "config.toml"},
		{"by its profile", "", "profile", codexProfileFile},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, _ := codexHome(t, c.auth, "")
			if c.scope != "" {
				stash(map[string]string{codexScopeKey: c.scope})
			}
			sub := provider.Provider{ID: "sub", Name: "Sub", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"old", "keep"}}
			if err := provider.Save(sub); err != nil {
				t.Fatal(err)
			}
			if err := codex(home).Fields[0].Set("sub/old"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, ".codex", c.file)
			model := func() string { v, _ := edit.GetTOMLTop(path, "model"); return v }
			if model() != "sub/old" {
				t.Fatalf("not set to begin with:\n%s", readFile(path))
			}

			// another of the provider's picks taken out: Codex stays
			sub.Models = []string{"old", "new"}
			if err := provider.Save(sub); err != nil {
				t.Fatal(err)
			}
			SyncCatalog()
			if got := model(); got != "sub/old" {
				t.Fatalf("model = %q while sub/old is still picked", got)
			}

			// the one Codex is on taken out, a new one picked
			sub.Models = []string{"new", "keep"}
			if err := provider.Save(sub); err != nil {
				t.Fatal(err)
			}
			SyncCatalog()
			if got := model(); got != "sub/new" {
				t.Fatalf("model = %q after sub/old was unchecked, want the provider's first pick sub/new:\n%s", got, readFile(path))
			}
			if l := readFile(filepath.Join(home, ".codex", "magpie-models.json")); c.auth == "" && (!strings.Contains(l, `"sub/new"`) || strings.Contains(l, `"sub/old"`)) {
				t.Errorf("Codex's list is not the provider's picks:\n%s", l)
			}
		})
	}
}

// A provider with no picks lists the vendor's models, and a request may name
// one its list lacks (#2642): Codex on such a model stays on it.
func TestCodexKeepsAModelOfAProviderWithNoPicks(t *testing.T) {
	home := syncHome(t)
	if err := provider.Save(provider.Provider{ID: "open", Name: "Open", Key: "k", Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "sub", Name: "Sub", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if err := codex(home).Fields[0].Set("open/unlisted"); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	path := filepath.Join(home, ".codex", "config.toml")
	if v, _ := edit.GetTOMLTop(path, "model"); v != "open/unlisted" {
		t.Errorf("model = %q, want open/unlisted kept:\n%s", v, readFile(path))
	}
	if pid, ok := provider.Unpicked("open/unlisted"); ok {
		t.Errorf("Unpicked(open/unlisted) = %q, true for a provider with no picks", pid)
	}
}
