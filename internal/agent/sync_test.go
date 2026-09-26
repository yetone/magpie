package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/provider"
)

// syncHome is a sandbox home with a models.dev catalog that knows glm-4.6's
// window and a provider serving it.
func syncHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("HERMES_HOME", "")
	t.Setenv("DSH_HOME", "")
	noKeychain(t)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"zai":{"models":{"glm-4.6":{"id":"glm-4.6","name":"GLM-4.6","limit":{"context":204800}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"glm-4.6"}}); err != nil {
		t.Fatal(err)
	}
	return home
}

// noKeychain puts a `security` that finds nothing first on PATH: the
// catalog looks for a Claude Code sign-in, on a Mac in the Keychain, which
// a test has no business reading.
func noKeychain(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "security"), []byte("#!/bin/sh\nexit 44\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// Pi is handed each model's window: without one it takes every model for a
// 128K one.
func TestPiModelsCarryContextWindow(t *testing.T) {
	syncHome(t)
	b, _ := json.Marshal(magpieProviderJSON("pi"))
	if !strings.Contains(string(b), `"id":"relay/glm-4.6"`) || !strings.Contains(string(b), `"contextWindow":204800`) {
		t.Fatalf("%s", b)
	}
}

// Pi is handed how long a reply may be: without it Pi caps every model at
// 16384 tokens. A model whose output isn't known leaves Pi its default.
func TestPiModelsCarryMaxTokens(t *testing.T) {
	syncHome(t)
	os.WriteFile(catalog.CachePath(), []byte(`{"zai":{"models":{"glm-4.6":{"id":"glm-4.6","name":"GLM-4.6","limit":{"context":204800,"output":131072}}}}}`), 0o644)
	catalog.Reset()
	b, _ := json.Marshal(magpieProviderJSON("pi"))
	if !strings.Contains(string(b), `"maxTokens":131072`) {
		t.Fatalf("%s", b)
	}

	os.WriteFile(catalog.CachePath(), []byte(`{"zai":{"models":{"glm-4.6":{"id":"glm-4.6","name":"GLM-4.6","limit":{"context":204800}}}}}`), 0o644)
	catalog.Reset()
	b, _ = json.Marshal(magpieProviderJSON("pi"))
	if strings.Contains(string(b), "maxTokens") {
		t.Fatalf("unknown output sent: %s", b)
	}
}

// A provider added after a magpie model was picked reaches the lists agents
// keep of magpie's models; a file magpie wrote nothing into stays as it is.
func TestSyncCatalogRewritesAgentLists(t *testing.T) {
	home := syncHome(t)
	piModels := filepath.Join(home, ".pi", "agent", "models.json")
	writeFile(t, piModels, `{"providers":{"mine":{"baseUrl":"http://x"},"magpie":{"name":"magpie","models":[]}}}`)
	crushCfg := filepath.Join(home, ".config", "crush", "crush.json")
	crushBody := `{"providers":{"mine":{"base_url":"http://x"}}}`
	writeFile(t, crushCfg, crushBody)
	codexDir := filepath.Join(home, ".codex")
	codexCat := filepath.Join(codexDir, "magpie-models.json")
	writeFile(t, filepath.Join(codexDir, "config.toml"), "model = \"relay/glm-4.6\"\nmodel_provider = \"magpie\"\nmodel_catalog_json = \""+codexCat+"\"\n")
	writeFile(t, codexCat, `{"models":[]}`)

	if err := provider.Save(provider.Provider{ID: "added", Name: "Added", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()

	pi := strings.ReplaceAll(readFile(piModels), " ", "")
	if !strings.Contains(pi, `"relay/glm-4.6"`) || !strings.Contains(pi, `"added/m2"`) ||
		!strings.Contains(pi, `"contextWindow":204800`) || !strings.Contains(pi, `"mine"`) {
		t.Errorf("pi models.json:\n%s", pi)
	}
	if got := readFile(crushCfg); got != crushBody {
		t.Errorf("crush config without magpie changed:\n%s", got)
	}
	cx := readFile(codexCat)
	if !strings.Contains(cx, `"slug": "added/m2"`) || !strings.Contains(cx, `"context_window": 204800`) {
		t.Errorf("codex catalog:\n%s", cx)
	}

	// nothing changed since: nothing is written
	st, _ := os.Stat(piModels)
	os.Chtimes(piModels, st.ModTime().Add(-1e12), st.ModTime().Add(-1e12))
	was, _ := os.Stat(piModels)
	SyncCatalog()
	if now, _ := os.Stat(piModels); !now.ModTime().Equal(was.ModTime()) {
		t.Error("pi models.json rewritten though nothing changed")
	}
}

// Signed in, Codex asks the gateway for its list and keeps it until it
// ages; a cache from before magpie's list changed is aged at once, one of
// the list as it is stays as it is.
func TestSyncCatalogAgesCodexCache(t *testing.T) {
	home := syncHome(t)
	dir := filepath.Join(home, ".codex")
	writeFile(t, filepath.Join(dir, "config.toml"), "model = \"relay/glm-4.6\"\nopenai_base_url = \""+codexGatewayURL()+"\"\n")
	cache := filepath.Join(dir, "models_cache.json")
	writeFile(t, cache, `{"fetched_at":"2026-09-25T10:00:00Z","etag":"W/\"v1\"","client_version":"0.155.1","models":[{"slug":"gpt-5.5"}]}`)
	SyncCatalog()
	var c struct {
		At      string           `json:"fetched_at"`
		ETag    string           `json:"etag"`
		Version string           `json:"client_version"`
		Models  []map[string]any `json:"models"`
	}
	json.Unmarshal([]byte(readFile(cache)), &c)
	if c.At != "1970-01-01T00:00:00Z" || c.ETag != `W/"v1"` || c.Version != "0.155.1" || len(c.Models) != 1 {
		t.Errorf("stale cache: %+v", c)
	}

	fresh := `{"fetched_at":"2026-09-25T10:00:00Z","etag":` + string(must(json.Marshal(codexcat.WithTag(`W/"v1"`, codexcat.Tag(provider.CodexListed()))))) + `,"models":[]}`
	writeFile(t, cache, fresh)
	SyncCatalog()
	if got := readFile(cache); got != fresh {
		t.Errorf("current cache changed:\n%s", got)
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// OpenCode is handed each model's window, so it compacts when the model
// needs it; and a sync puts magpie's provider back when a model of
// magpie's is chosen but the provider is gone from the file.
func TestOpenCodeModelsCarryContextAndSyncRestores(t *testing.T) {
	home := syncHome(t)
	b, _ := json.Marshal(magpieProviderJSON("opencode"))
	if !strings.Contains(string(b), `"relay/glm-4.6":{`) || !strings.Contains(string(b), `"limit":{"context":204800,"output":0}`) {
		t.Fatalf("%s", b)
	}
	cfg := filepath.Join(home, ".config", "opencode", "opencode.json")
	writeFile(t, cfg, `{"model":"magpie/relay/glm-4.6","provider":{"mine":{"name":"mine"}}}`)
	if err := opencode(home, filepath.Join(home, ".config")).Sync(); err != nil {
		t.Fatal(err)
	}
	if s := readFile(cfg); !strings.Contains(s, `"magpie"`) || !strings.Contains(s, `"mine"`) || !strings.Contains(s, `204800`) {
		t.Fatalf("%s", s)
	}
	// one that doesn't use magpie gets nothing
	body := `{"model":"mine/x","provider":{"mine":{"name":"mine"}}}`
	writeFile(t, cfg, body)
	if err := opencode(home, filepath.Join(home, ".config")).Sync(); err != nil {
		t.Fatal(err)
	}
	if s := readFile(cfg); s != body {
		t.Fatalf("%s", s)
	}
}
