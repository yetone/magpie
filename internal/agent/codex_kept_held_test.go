package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// davezfr on X, after v0.1.4216: Codex signed in to account 1, which had
// used up both its allowance and its credits (held), showed "magpie" as
// its account in the Codex app; picked in Codex's sign-in, account 2,
// with room, showed itself. magpie became Codex's provider for the held
// account (codex.out), and the app-server answers account/read with no
// account for a provider without requires_openai_auth. The account the
// user keeps Codex on is theirs to see, held or not: Sync leaves Codex
// beside that sign-in, and a takeover made before the user kept it ends.
func TestCodexKeptOnAHeldAccountShowsIt(t *testing.T) {
	kept := false
	codexSignInKept = func() bool { return kept }
	t.Cleanup(func() { codexSignInKept = func() bool { return false } })

	for _, c := range []struct {
		name, config, model string
		setup               func(t *testing.T, home string, cx *Agent)
	}{
		{name: "magpie's model", model: "fake/m1", setup: func(t *testing.T, _ string, cx *Agent) {
			if err := cx.Fields[0].Set("fake/m1"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "Codex's own model", config: "model = \"gpt-a\"\n", model: "gpt-a", setup: func(t *testing.T, home string, cx *Agent) {
			if err := provider.Save(provider.Provider{ID: "codex", Fallback: []string{"fake/m1"}}); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"),
				[]byte(`{"models":[{"slug":"gpt-a","display_name":"A","priority":1}]}`), 0o644)
			if ok, err := cx.Join(); err != nil || !ok {
				t.Fatalf("join: %v %v", ok, err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, c.config)
			cx := codex(home)
			c.setup(t, home, cx)
			beside := func(when string) {
				t.Helper()
				if cfg := read(); strings.Contains(cfg, "model_provider =") || strings.Contains(cfg, "model_catalog_json") ||
					!strings.Contains(cfg, "openai_base_url") {
					t.Fatalf("%s: not beside the sign-in:\n%s", when, cfg)
				}
				if cx.AppOut() {
					t.Fatalf("%s: AppOut", when)
				}
			}

			// held, not kept: the takeover, as before
			codexUsedUp = func() bool { return true }
			kept = false
			if err := cx.Sync(); err != nil {
				t.Fatal(err)
			}
			if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) || !cx.AppOut() {
				t.Fatalf("held, not kept: no takeover:\n%s", cfg)
			}

			// the user keeps Codex on that account: back beside its sign-in,
			// where the app shows the account
			kept = true
			if err := cx.Sync(); err != nil {
				t.Fatal(err)
			}
			beside("held, kept")
			if err := cx.Sync(); err != nil {
				t.Fatal(err)
			}
			beside("held, kept, synced again")
			if c.model == "fake/m1" {
				// a pick of magpie's model on it doesn't take it over either
				if err := cx.Fields[0].Set("fake/m1"); err != nil {
					t.Fatal(err)
				}
				beside("held, kept, picked")
			}
			if cfg := read(); !strings.Contains(cfg, `model = "`+c.model+`"`) {
				t.Fatalf("held, kept: model changed:\n%s", cfg)
			}

			// let go of, it is the takeover's again
			kept = false
			if err := cx.Sync(); err != nil {
				t.Fatal(err)
			}
			if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) || !cx.AppOut() {
				t.Fatalf("held, let go: no takeover:\n%s", cfg)
			}
		})
	}
}
