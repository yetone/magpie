package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// agentFiles reads every file under home but magpie's own (its settings,
// stash and caches): what an agent reads its config from.
func agentFiles(t *testing.T, home string) map[string]string {
	t.Helper()
	files := map[string]string{}
	filepath.WalkDir(home, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(home, p)
		if e.IsDir() {
			if rel == filepath.Join(".config", "magpie") || rel == ".cache" {
				return filepath.SkipDir
			}
			return nil
		}
		b, _ := os.ReadFile(p)
		files[rel] = string(b)
		return nil
	})
	return files
}

// A disconnect the user chose stays through magpie's start-up sync, which
// runs at every start and after every update (SyncCatalog): no agent is
// wired again, and none of its files changes (paynezhuang on Discord: Codex
// had magpie's base URL back after each update). So does it through what a
// running gateway keeps up (dsh's routes).
func TestDisconnectStaysThroughSync(t *testing.T) {
	home, _ := codexHome(t, "", "")
	managed := claudeManaged
	claudeManaged = func() string { return filepath.Join(home, "managed-settings.json") }
	t.Cleanup(func() { claudeManaged = managed })
	startAlma(t)    // Alma keeps its providers in the app
	startShelley(t) // so does Shelley its models
	os.MkdirAll(filepath.Join(home, ".hanako", "agents", "hana"), 0o755)
	os.WriteFile(filepath.Join(home, ".hanako", "agents", "hana", "config.yaml"), []byte("agent:\n  name: Hana\n"), 0o644)
	for _, a := range All() {
		if a.Launch != nil || a.Native != nil || a.WSL != "" || len(a.Fields) == 0 {
			continue
		}
		t.Run(a.ID, func(t *testing.T) {
			if _, err := a.ConnectHow(); err != nil {
				t.Skip("can't be connected here:", err)
			}
			if !a.Wired() {
				t.Skip("not wired by Connect here")
			}
			if err := a.Disconnect(); err != nil {
				t.Fatal(err)
			}
			if a.Wired() {
				t.Fatal("still wired after Disconnect")
			}
			before := agentFiles(t, home)
			for i := 0; i < 2; i++ {
				SyncCatalog()
				// and what a running gateway keeps up (KeepDshWired)
				dshWiredOnce()
				if a.Wired() || a.FailingOver != nil && a.FailingOver() {
					t.Fatalf("sync %d wired it again", i)
				}
			}
			after := agentFiles(t, home)
			for p, b := range before {
				if after[p] != b {
					t.Errorf("sync changed %s:\n--- after Disconnect\n%s\n--- after sync\n%s", p, b, after[p])
				}
			}
			for p := range after {
				if _, ok := before[p]; !ok {
					t.Errorf("sync wrote %s", p)
				}
			}
		})
	}
}

// codexChatGPTAuth is a Codex auth.json signed in to ChatGPT as email.
func codexChatGPTAuth(email, acct string) map[string]any {
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":      claims(map[string]any{"email": email}),
		"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
		"refresh_token": "r-" + acct, "account_id": acct}}
}

// paynezhuang on Discord: Codex signed in to ChatGPT, with another of the
// user's ChatGPT accounts on in magpie. magpie's first start pointed Codex
// at the gateway for failover; the user switched Codex on, then chose
// "Disconnect from magpie", and after each update and restart Codex had
// magpie's base URL in config.toml again. A disconnect is the user's say:
// no start brings it back, with ChatGPT sign-in or magpie API, until the
// user connects Codex again.
func TestCodexDisconnectStaysWithAccountsOn(t *testing.T) {
	for _, login := range []string{"", "api"} {
		t.Run("login="+login, func(t *testing.T) {
			me, _ := json.Marshal(codexChatGPTAuth("me@example.com", "acct-1"))
			const own = "model = \"gpt-5.5\"\nmodel_reasoning_effort = \"high\"\n\n[projects.\"/Users/me/code\"]\ntrust_level = \"trusted\"\n"
			home, read := codexHome(t, string(me), own)
			b, _ := json.Marshal([]map[string]any{{"agent": "codex", "user": "spare@example.com", "on": true,
				"seen": time.Now(), "auth": codexChatGPTAuth("spare@example.com", "acct-2")}})
			os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
			os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), b, 0o600)
			cx := codex(home)
			// magpie's first start: failover points Codex at the gateway
			SyncCatalog()
			if cfg := read(); !strings.Contains(cfg, "openai_base_url") {
				t.Fatalf("first start, two accounts on:\n%s", cfg)
			}
			if login != "" {
				if err := cx.Apply("login", login); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := cx.ConnectHow(); err != nil {
				t.Fatal(err)
			}
			if !cx.Wired() {
				t.Fatalf("not connected:\n%s", read())
			}
			if err := cx.Disconnect(); err != nil {
				t.Fatal(err)
			}
			restored := read()
			if strings.Contains(restored, "openai_base_url") || strings.Contains(restored, "model_provider =") {
				t.Fatalf("disconnect left magpie in:\n%s", restored)
			}
			// restarts, an update among them
			for i := 0; i < 3; i++ {
				SyncCatalog()
				if cfg := read(); cfg != restored {
					t.Fatalf("start %d after Disconnect wired Codex again:\n--- disconnected\n%s\n--- now\n%s", i, restored, cfg)
				}
				if cx.Wired() || cx.FailingOver() {
					t.Fatalf("start %d: wired %v, failing over %v", i, cx.Wired(), cx.FailingOver())
				}
			}
			// a pick of one of Codex's own models in magpie keeps it so
			if login == "" {
				if err := cx.Pick("model", "gpt-5.4"); err != nil {
					t.Fatal(err)
				}
				if cfg := read(); strings.Contains(cfg, "openai_base_url") {
					t.Fatalf("own model picked after Disconnect:\n%s", cfg)
				}
			}
			// connected again, it is magpie's again, and a start keeps it so
			if _, err := cx.ConnectHow(); err != nil {
				t.Fatal(err)
			}
			SyncCatalog()
			if !cx.Wired() {
				t.Fatalf("connected again, then a start:\n%s", read())
			}
		})
	}
}

// On magpie API, Codex's own model served on its ChatGPT account (#701's
// config): Sync wires a Codex left unwired so, but not one the user
// disconnected.
func TestCodexAPIDisconnectStays(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "model = \"gpt-a\"\n")
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"models":[{"slug":"gpt-a","display_name":"A","priority":1}]}`), 0o644)
	cx := codex(home)
	if err := cx.Field("login").Set("api"); err != nil {
		t.Fatal(err)
	}
	if !cx.Wired() {
		t.Fatalf("magpie API not wired:\n%s", read())
	}
	if err := cx.Disconnect(); err != nil {
		t.Fatal(err)
	}
	restored := read()
	if cx.Wired() || strings.Contains(restored, "model_provider =") || strings.Contains(restored, "openai_base_url") {
		t.Fatalf("disconnect left magpie in:\n%s", restored)
	}
	for i := 0; i < 2; i++ {
		SyncCatalog()
		if cfg := read(); cfg != restored || cx.Wired() {
			t.Fatalf("start %d after Disconnect wired Codex again:\n--- disconnected\n%s\n--- now\n%s", i, restored, cfg)
		}
	}
	// connected again by the user, magpie API wires it as before
	if _, err := cx.ConnectHow(); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	if cfg := read(); !cx.Wired() || !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("connected again:\n%s", cfg)
	}
}
