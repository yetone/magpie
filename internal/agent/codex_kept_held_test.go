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
// account for a provider without requires_openai_auth. On one of Codex's
// own models the account the user keeps Codex on is theirs to see, held
// or not: Sync leaves Codex beside that sign-in, and a takeover made
// before the user kept it ends. On a model of magpie's the takeover
// stands, kept or not (TestCodexKeptOnAHeldAccountSendsMagpiesModel).
func TestCodexKeptOnAHeldAccountShowsIt(t *testing.T) {
	kept := false
	codexSignInKept = func() bool { return kept }
	t.Cleanup(func() { codexSignInKept = func() bool { return false } })

	for _, c := range []struct {
		name, config, model string
		setup               func(t *testing.T, home string, cx *Agent)
	}{
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

// Yuzhou__Zhang on X, after updating from Stable (v0.1.3960) to v0.1.4477:
// with the ChatGPT account used up, the Codex app sent nothing, on a
// routing group of Claude accounts with room too, and showed only the
// account's "usage used up" banner. a5e3972a7 left the account the user
// keeps Codex on (Switch account automatically off) beside its sign-in
// whatever the model, and beside it the app greys Send for every model
// till the account's reset. On a model of magpie's, magpie is Codex's
// provider while the account is held, kept or not, as before a5e3972a7:
// the takeover Sync makes, the one a pick makes, and one made before.
func TestCodexKeptOnAHeldAccountSendsMagpiesModel(t *testing.T) {
	kept := true
	codexSignInKept = func() bool { return kept }
	t.Cleanup(func() {
		codexSignInKept = func() bool { return false }
		codexUsedUp = func() bool { return false }
	})
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	cx := codex(home)
	takenOver := func(when string) {
		t.Helper()
		if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) || !strings.Contains(cfg, `model = "fake/m1"`) || !cx.AppOut() {
			t.Fatalf("%s: magpie's model left on the held account, where the app greys Send:\n%s", when, cfg)
		}
	}

	// picked while the account has room: beside the sign-in
	codexUsedUp = func() bool { return false }
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider =") || cx.AppOut() {
		t.Fatalf("room: taken over:\n%s", cfg)
	}

	// the kept account runs out: the takeover, and it stays
	codexUsedUp = func() bool { return true }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	takenOver("held, kept")
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	takenOver("held, kept, synced again")

	// picked again while held: the takeover too
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	takenOver("held, kept, picked")

	// the account has room again: back beside its sign-in
	codexUsedUp = func() bool { return false }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider =") || !strings.Contains(cfg, "openai_base_url") || cx.AppOut() {
		t.Fatalf("room again: still taken over:\n%s", cfg)
	}

	// Always ChatGPT still keeps it beside the sign-in
	codexUsedUp = func() bool { return true }
	stash(map[string]string{here(home).key("codex.login"): "chatgpt"})
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider =") || cx.AppOut() {
		t.Fatalf("Always ChatGPT: taken over:\n%s", cfg)
	}
}
