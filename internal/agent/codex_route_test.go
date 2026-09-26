package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func codexHome(t *testing.T, auth, config string) (home string, read func() string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", "")
	noKeychain(t)
	usedUp := codexUsedUp
	codexUsedUp = func() bool { return false }
	t.Cleanup(func() { codexUsedUp = usedUp })
	dir := filepath.Join(home, ".codex")
	os.MkdirAll(dir, 0o755)
	if auth != "" {
		os.WriteFile(filepath.Join(dir, "auth.json"), []byte(auth), 0o600)
	}
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o644)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	return home, func() string {
		b, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
		return string(b)
	}
}

// Signed in, a magpie model points Codex's built-in OpenAI provider at
// magpie and leaves the provider as it is; one of Codex's own models takes
// the base URL away again and brings back what was there.
func TestCodexSignedInRoutesByBaseURL(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`,
		"model = \"gpt-5.5\"\nmodel_reasoning_effort = \"xhigh\"\n\n[projects.\"/x\"]\ntrust_level = \"trusted\"\n")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if !strings.Contains(cfg, `openai_base_url = "http://127.0.0.1:`) || !strings.Contains(cfg, `/backend-api/codex"`) ||
		!strings.Contains(cfg, `model = "fake/m1"`) || strings.Contains(cfg, "model_provider") ||
		strings.Contains(cfg, "model_catalog_json") || strings.Contains(cfg, "[model_providers") {
		t.Fatalf("magpie model:\n%s", cfg)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "magpie-models.json")); err == nil {
		t.Error("catalog file written")
	}
	if err := cx.Fields[0].Set("gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	cfg = read()
	if strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model = "gpt-5.4"`) ||
		!strings.Contains(cfg, `model_reasoning_effort = "xhigh"`) || !strings.Contains(cfg, `trust_level = "trusted"`) {
		t.Fatalf("own model:\n%s", cfg)
	}
}

// A magpie set up as a provider of Codex's, from before, moves to the base
// URL the next time a magpie model is picked.
func TestCodexSignedInLeavesProviderTable(t *testing.T) {
	home, read := codexHome(t, `{"OPENAI_API_KEY":"sk-x"}`,
		"model = \"fake/m1\"\nmodel_provider = \"magpie\"\nmodel_catalog_json = \"/x/magpie-models.json\"\n\n[model_providers.magpie]\nname = \"magpie\"\nbase_url = \"http://127.0.0.1:3425/v1\"\nwire_api = \"responses\"\n")
	if err := codex(home).Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if !strings.Contains(cfg, "openai_base_url") || strings.Contains(cfg, "model_provider") || strings.Contains(cfg, "model_catalog_json") {
		t.Fatalf("\n%s", cfg)
	}
}

// A provider table the user wrote with spaces around the dots — valid TOML —
// is the same table to magpie: it is reused in place, and taken away again
// when Codex steps back to its own models, as if magpie had written it.
func TestCodexSpacedProviderTable(t *testing.T) {
	home, read := codexHome(t, "", "model = \"gpt-5.5\"\n\n[ model_providers . magpie ]\nname = \"magpie\"\nbase_url = \"http://127.0.0.1:1/v1\"\n\n[[skills.config]]\npath = \"/skill\"\n")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if strings.Count(cfg, "model_providers") != 1 || !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("provider table was not reused:\n%s", cfg)
	}
	if err := cx.Fields[0].Set(""); err != nil {
		t.Fatal(err)
	}
	if cfg = read(); strings.Contains(cfg, "magpie") || !strings.Contains(cfg, "[[skills.config]]") {
		t.Fatalf("reset:\n%s", cfg)
	}
}

// Not signed in, Codex's OpenAI provider can't run, so magpie is a provider
// of its own.
func TestCodexSignedOutUsesProvider(t *testing.T) {
	home, read := codexHome(t, "", "")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model_provider = "magpie"`) ||
		!strings.Contains(cfg, "[model_providers.magpie]") || !strings.Contains(cfg, "model_catalog_json") {
		t.Fatalf("\n%s", cfg)
	}
	if err := cx.Fields[0].Set(""); err != nil {
		t.Fatal(err)
	}
	if cfg = read(); strings.Contains(cfg, "magpie") || strings.Contains(cfg, "model") {
		t.Fatalf("reset:\n%s", cfg)
	}
}

// A ChatGPT account that has used its allowance up keeps the Codex app from
// sending, so magpie is a provider of Codex's then, as when signed out.
func TestCodexUsedUpUsesProvider(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	codexUsedUp = func() bool { return true }
	if err := codex(home).Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model_provider = "magpie"`) ||
		!strings.Contains(cfg, "[model_providers.magpie]") || !strings.Contains(cfg, `model = "fake/m1"`) {
		t.Fatalf("\n%s", cfg)
	}
}

// Subagents can be given a magpie model of their own once Codex runs through
// magpie; it goes with magpie when Codex steps back to its own models, where
// a user's own choice of one of those stays.
func TestCodexSubagentModel(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`,
		"model = \"gpt-5.5\"\n\n[agents]\nmax_threads = 6\n")
	cx := codex(home)
	sub := cx.Field("subagent")
	if sub == nil {
		t.Fatal("no subagent field")
	}
	if err := sub.Set("fake/m1"); err == nil {
		t.Error("a magpie model was taken before Codex runs through magpie")
	}
	for _, o := range sub.Options(nil) {
		if isMagpie(o.Value) {
			t.Errorf("magpie model offered before Codex runs through magpie: %s", o.Value)
		}
	}
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := sub.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if got := sub.Get(); got != "fake/m1" {
		t.Fatalf("subagent = %q\n%s", got, read())
	}
	if cfg := read(); !strings.Contains(cfg, "max_threads = 6") {
		t.Fatalf("user's [agents] key lost:\n%s", cfg)
	}
	if err := cx.Fields[0].Set("gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "default_subagent_model") || !strings.Contains(cfg, "max_threads = 6") {
		t.Fatalf("stepping out of magpie:\n%s", cfg)
	}
	if err := sub.Set("gpt-5.4-mini"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Fields[0].Set(""); err != nil {
		t.Fatal(err)
	}
	if got := sub.Get(); got != "gpt-5.4-mini" {
		t.Fatalf("own subagent model dropped: %q", got)
	}
	if err := sub.Set(""); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "default_subagent_model") {
		t.Fatalf("\n%s", cfg)
	}
}

func TestCodexSubagentReadErrorStopsChanges(t *testing.T) {
	const input = "model = \"fake/m1\"\nmodel_provider = \"magpie\"\n\n[agents]\ndefault_subagent_model = [\n"
	for _, model := range []string{"", "gpt-native"} {
		t.Run("model="+model, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, ".codex", "config.toml")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			err := codex(home).Field("model").Set(model)
			if err == nil || !strings.HasPrefix(err.Error(), path+": ") {
				t.Fatalf("expected the config path in the error, got %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != input {
				t.Fatalf("changed config after a subagent read error:\n%s", got)
			}
		})
	}
}

func TestCodexCheckReportsTOMLParseErrors(t *testing.T) {
	for _, profile := range []string{"", "profile = \"work\"\n"} {
		input := "model = \"fake/m1\"\nmodel_provider = \"magpie\"\n" + profile + "\n[model_providers.magpie]\ninvalid = [\n"
		home, read := codexHome(t, "", input)
		path := filepath.Join(home, ".codex", "config.toml")
		message := codex(home).Check()
		if !strings.HasPrefix(message, path+": line ") || !strings.Contains(message, "column") || !strings.Contains(message, "array is incomplete") {
			t.Fatalf("check did not report the parse location: %q", message)
		}
		if read() != input {
			t.Fatal("check changed the config")
		}
	}
}
