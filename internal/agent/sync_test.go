package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"gopkg.in/yaml.v3"
)

// syncHome is a sandbox home with a models.dev catalog that knows glm-4.6's
// window and a provider serving it.
func syncHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("HERMES_HOME", "")
	t.Setenv("MIMOCODE_HOME", "")
	t.Setenv("HANA_HOME", "")
	t.Setenv("DSH_HOME", "")
	t.Setenv("OMO_CODING_AGENT_DIR", "")
	t.Setenv("SENPI_CODING_AGENT_DIR", "")
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("PI_CONFIG_DIR", "")
	t.Setenv("OMP_PROFILE", "")
	t.Setenv("PI_PROFILE", "")
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

// Crush is handed how long a reply may be beside the window it is handed
// with it: without it Crush caps every model at 16384 tokens, one the
// catalogue says can write far more of them included. A model whose output
// isn't known keeps Crush's own default.
func TestCrushModelsCarryMaxTokens(t *testing.T) {
	syncHome(t)
	os.WriteFile(catalog.CachePath(), []byte(`{"zai":{"models":{"glm-4.6":{"id":"glm-4.6","name":"GLM-4.6","limit":{"context":204800,"output":131072}}}}}`), 0o644)
	catalog.Reset()
	b, _ := json.Marshal(magpieProviderJSON("crush"))
	if !strings.Contains(string(b), `"id":"relay/glm-4.6"`) || !strings.Contains(string(b), `"context_window":204800`) ||
		!strings.Contains(string(b), `"default_max_tokens":131072`) {
		t.Fatalf("%s", b)
	}

	os.WriteFile(catalog.CachePath(), []byte(`{"zai":{"models":{"glm-4.6":{"id":"glm-4.6","name":"GLM-4.6","limit":{"context":204800}}}}}`), 0o644)
	catalog.Reset()
	b, _ = json.Marshal(magpieProviderJSON("crush"))
	if !strings.Contains(string(b), `"default_max_tokens":16384`) {
		t.Fatalf("unknown output took Crush's default away: %s", b)
	}
}

// An output limit above the model's window (models.dev lists deepseek-chat's
// 384000 against 128000 of context) is cut to the window for every agent
// magpie hands an output limit; one whose window isn't known keeps its
// output. ZCode and WorkBuddy cap it at zcodeMaxOutput besides, Crush falls
// back to 16384 without a known output, and OpenCode is handed no limit
// without a window.
func TestMaxTokensWithinContextWindow(t *testing.T) {
	home := syncHome(t)
	check := func(limit string, want int) {
		t.Helper()
		os.WriteFile(catalog.CachePath(), []byte(`{"zai":{"models":{"glm-4.6":{"id":"glm-4.6","name":"GLM-4.6","limit":{`+limit+`}}}}}`), 0o644)
		catalog.Reset()
		pi, _ := json.Marshal(magpieProviderJSON("pi"))
		cline, _ := json.Marshal(clineModels(""))
		omp, _ := yaml.Marshal(ompProvider())
		dshRoute, _ := yaml.Marshal(dshRouteConfig(magpieModels("dsh"), ""))
		droid, _ := json.Marshal(droidEntries())
		qoder, _ := json.Marshal(qoderProvider("qoder", ""))
		hanako, _ := json.Marshal(hanakoProvider())
		opencode, _ := json.Marshal(magpieProviderJSON("opencode"))
		zc, _ := json.Marshal(zcodeProviderJSON(filepath.Join(home, "none.json")))
		crush, _ := json.Marshal(magpieProviderJSON("crush"))
		rules, wb := filepath.Join(t.TempDir(), "provider_config.json"), filepath.Join(t.TempDir(), "models.json")
		if err := zcodeRules(rules, true); err != nil {
			t.Fatal(err)
		}
		if err := workbuddyWrite(wb, true); err != nil {
			t.Fatal(err)
		}
		zcRules, _ := os.ReadFile(rules)
		wbModels, _ := os.ReadFile(wb)
		n, capped := strconv.Itoa(want), strconv.Itoa(min(want, zcodeMaxOutput))
		wants := map[string][2]string{
			"pi":          {string(pi), `"maxTokens":` + n},
			"cline":       {string(cline), `"maxTokens":` + n},
			"omp":         {string(omp), "maxTokens: " + n},
			"dsh":         {string(dshRoute), "maxTokens: " + n},
			"droid":       {string(droid), `"maxOutputTokens":` + n},
			"qoder":       {string(qoder), `"maxOutputTokens":` + n},
			"hanako":      {string(hanako), `"maxOutput":` + n},
			"zcode":       {string(zc), `"output":` + capped},
			"crush":       {string(crush), `"default_max_tokens":` + n},
			"zcode rules": {string(zcRules), `"max":` + capped},
			"workbuddy":   {string(wbModels), `"maxOutputTokens": ` + capped},
		}
		if strings.Contains(limit, "context") {
			wants["opencode"] = [2]string{string(opencode), `"output":` + n}
		}
		for agent, w := range wants {
			if !strings.Contains(w[0], w[1]) {
				t.Errorf("%s: want %s in %s", agent, w[1], w[0])
			}
		}
	}
	check(`"context":128000,"output":384000`, 128000)
	check(`"output":384000`, 384000)
	check(`"context":204800,"output":131072`, 131072)
	check(`"context":64000,"output":384000`, 64000)
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
	writeFile(t, filepath.Join(codexDir, "config.toml"), "model = \"relay/glm-4.6\"\nmodel_provider = \"magpie\"\nmodel_catalog_json = '"+codexCat+"'\n")
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
	writeFile(t, filepath.Join(dir, "auth.json"), `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`)
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

// A model of magpie's chosen for OpenCode that a provider of the file's own
// already sends to magpie is named there, and magpie's flat provider isn't
// added beside it with the same models again.
func TestOpenCodeKeepsItsOwnGatewayProviders(t *testing.T) {
	home := syncHome(t)
	cfg := filepath.Join(home, ".config", "opencode", "opencode.json")
	writeFile(t, cfg, `{
  "provider": {
    "magpie-relay": {"npm": "@ai-sdk/openai-compatible", "options": {"baseURL": "http://localhost:3425/v1/"}, "models": {"relay/glm-4.6": {}}},
    "elsewhere": {"options": {"baseURL": "https://example.com/v1"}, "models": {"relay/glm-4.6": {}}}
  },
  "model": "magpie-relay/relay/glm-4.6"
}`)
	oc := opencode(home, filepath.Join(home, ".config"))
	if err := oc.Fields[0].Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(cfg, "model"); v != "magpie-relay/relay/glm-4.6" {
		t.Fatalf("model = %q", v)
	}
	if _, ok := edit.GetJSON(cfg, "provider.magpie"); ok {
		t.Fatal("magpie's provider added beside the file's own")
	}
	// a model none of them lists still gets magpie's provider
	os.WriteFile(cfg, []byte(`{"provider": {"magpie-relay": {"options": {"baseURL": "http://127.0.0.1:3425/v1"}, "models": {}}}}`), 0o644)
	if err := oc.Fields[0].Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(cfg, "model"); v != "magpie/relay/glm-4.6" {
		t.Fatalf("model = %q", v)
	}
	if _, ok := edit.GetJSON(cfg, "provider.magpie"); !ok {
		t.Fatal("magpie's provider not added")
	}
}

// MiMo Code shares OpenCode's config shape: a sync writes magpie's provider
// into its own mimocode.json, and reads its own model list.
func TestMiMoCodeMirrorsOpenCode(t *testing.T) {
	home := syncHome(t)
	cfg := filepath.Join(home, ".config", "mimocode", "mimocode.json")
	writeFile(t, cfg, `{"model":"magpie/relay/glm-4.6","provider":{"mine":{"name":"mine"}}}`)
	if err := mimocode(home, filepath.Join(home, ".config")).Sync(); err != nil {
		t.Fatal(err)
	}
	s := readFile(cfg)
	if !strings.Contains(s, `"magpie"`) || !strings.Contains(s, `"mine"`) || !strings.Contains(s, `204800`) {
		t.Fatalf("%s", s)
	}
	// one that doesn't use magpie gets nothing
	body := `{"model":"mine/x","provider":{"mine":{"name":"mine"}}}`
	writeFile(t, cfg, body)
	if err := mimocode(home, filepath.Join(home, ".config")).Sync(); err != nil {
		t.Fatal(err)
	}
	if s := readFile(cfg); s != body {
		t.Fatalf("%s", s)
	}
}

// Xiaomi MiMo, the desktop app, runs MiMo Code's engine and reads its config
// as it does (#249): the first of mimocode.jsonc, mimocode.json and
// config.json there is, in $MIMOCODE_HOME/config when that is set.
func TestMiMoCodeConfigAsTheAppFindsIt(t *testing.T) {
	home := syncHome(t)
	plain := filepath.Join(home, ".config", "mimocode", "config.json")
	writeFile(t, plain, `{"model":"magpie/relay/glm-4.6","provider":{"mine":{"name":"mine"}}}`)
	a := mimocode(home, filepath.Join(home, ".config"))
	if a.Path != plain {
		t.Fatalf("config at %s, want %s", a.Path, plain)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if s := readFile(plain); !strings.Contains(s, `"magpie"`) || !strings.Contains(s, `"mine"`) {
		t.Fatalf("%s", s)
	}
	// its own name comes first
	own := filepath.Join(home, ".config", "mimocode", "mimocode.json")
	writeFile(t, own, `{}`)
	if a := mimocode(home, filepath.Join(home, ".config")); a.Path != own {
		t.Fatalf("config at %s, want %s", a.Path, own)
	}

	moved := filepath.Join(home, "mimo-home")
	t.Setenv("MIMOCODE_HOME", moved)
	if a := mimocode(home, filepath.Join(home, ".config")); a.Path != filepath.Join(moved, "config", "mimocode.json") {
		t.Fatalf("with MIMOCODE_HOME, config at %s", a.Path)
	}
	// a relative one is refused by the app, and ignored here
	t.Setenv("MIMOCODE_HOME", "mimo-home")
	if a := mimocode(home, filepath.Join(home, ".config")); a.Path != own {
		t.Fatalf("with a relative MIMOCODE_HOME, config at %s", a.Path)
	}
}

// magpieProviderJSONFor reads the catalog narrowed under the agent's own id,
// while the config shape stays OpenCode's: a visibility on mimocode narrows
// its provider block and leaves OpenCode's alone.
func TestMiMoCodeCatalogNarrowedSeparately(t *testing.T) {
	syncHome(t)
	if err := settings.Save(settings.Settings{Visible: map[string][]string{"mimocode": {"relay"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { settings.Save(settings.Settings{}) })
	mimo := magpieProviderJSONFor("opencode", "mimocode").(map[string]any)["models"].(map[string]any)
	oc := magpieProviderJSON("opencode").(map[string]any)["models"].(map[string]any)
	if len(mimo) != 1 || mimo["relay/glm-4.6"] == nil {
		t.Fatalf("mimocode catalog: %v", mimo)
	}
	if oc["relay/glm-4.6"] == nil {
		t.Fatalf("opencode catalog was narrowed too: %v", oc)
	}
}
