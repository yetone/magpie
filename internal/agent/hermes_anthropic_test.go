package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"gopkg.in/yaml.v3"
)

// hermesAnthropicHome is a sandbox HOME with a chat provider (deepseek)
// and one served on Anthropic's Messages (relay, Claude's models), and the
// path of Hermes's config there
func hermesAnthropicHome(t *testing.T) (home, path string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Anthropic: "https://relay.example/v1", Key: "k", Models: []string{"claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(appdir.HermesHomeIn(home), "config.yaml")
	os.MkdirAll(filepath.Dir(path), 0o755)
	return home, path
}

type hermesTestEntry struct {
	BaseURL   string   `yaml:"base_url"`
	APIKey    string   `yaml:"api_key"`
	APIMode   string   `yaml:"api_mode"`
	Transport string   `yaml:"transport"`
	Models    []string `yaml:"models"`
}

type hermesTestConfig struct {
	Model struct {
		Default  string `yaml:"default"`
		Provider string `yaml:"provider"`
	} `yaml:"model"`
	Providers map[string]*hermesTestEntry `yaml:"providers"`
}

func readHermes(t *testing.T, path string) (hermesTestConfig, string) {
	t.Helper()
	raw := readFile(path)
	var c hermesTestConfig
	if err := yaml.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	return c, raw
}

// #5315 (cherish_onlyy): Hermes takes the API from the provider entry a
// model is under, and magpie's one entry was chat completions for every
// model, so a Claude model's prompt_caching.cache_ttl: 1h never reached
// the vendor. A model served best on Messages goes under
// "magpie-anthropic", spoken to as anthropic_messages at the gateway's
// root; every other model stays under "magpie" on chat completions, so
// both work from one config.
func TestHermesClaudeGoesOnMessages(t *testing.T) {
	home, path := hermesAnthropicHome(t)
	os.WriteFile(path, []byte("model:\n  default: kimi-k3\n  provider: moonshot\n"), 0o644)
	bot := filepath.Join(filepath.Dir(path), "profiles", "coder", "config.yaml")
	os.MkdirAll(filepath.Dir(bot), 0o755)
	os.WriteFile(bot, []byte("model:\n  default: kimi-k3\n  provider: moonshot\n"), 0o644)
	setUpInMagpie("hermes")
	a := hermes(home)
	f := a.Field("model")
	if err := f.Set("magpie/relay/claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	gw := gateway.URL()
	for _, p := range []string{path, bot} {
		c, raw := readHermes(t, p)
		chat, anth := c.Providers[magpieID], c.Providers[hermesAnthropicID]
		if chat == nil || anth == nil {
			t.Fatalf("%s: entries:\n%s", p, raw)
		}
		if anth.APIMode != "anthropic_messages" || anth.BaseURL != gw || anth.APIKey == "" || !slices.Equal(anth.Models, []string{"relay/claude-opus-5-5"}) {
			t.Fatalf("%s: magpie-anthropic:\n%s", p, raw)
		}
		if chat.APIMode != "chat_completions" || chat.BaseURL != gw+"/v1" || !slices.Contains(chat.Models, "deepseek/pro") || slices.Contains(chat.Models, "relay/claude-opus-5-5") {
			t.Fatalf("%s: magpie:\n%s", p, raw)
		}
	}
	c, raw := readHermes(t, path)
	if c.Model.Provider != hermesAnthropicID || c.Model.Default != "relay/claude-opus-5-5" {
		t.Fatalf("model:\n%s", raw)
	}
	if f.Get() != "magpie/relay/claude-opus-5-5" || !a.Wired() || a.Drift() != nil || a.Check() != "" {
		t.Fatalf("get %q, wired %v, drift %+v, check %q", f.Get(), a.Wired(), a.Drift(), a.Check())
	}
	// another of magpie's models, on chat: under "magpie"
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if c, raw = readHermes(t, path); c.Model.Provider != magpieID || c.Model.Default != "deepseek/pro" {
		t.Fatalf("chat model:\n%s", raw)
	}
	if f.Get() != "magpie/deepseek/pro" || a.Check() != "" {
		t.Fatalf("get %q, check %q", f.Get(), a.Check())
	}
	// a bot picked onto Claude on the Agents page goes under it too
	if err := a.Field(hermesProfileKey + "coder").Set("magpie/relay/claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if c, raw = readHermes(t, bot); c.Model.Provider != hermesAnthropicID {
		t.Fatalf("bot:\n%s", raw)
	}
	// Disconnect takes both entries out of every config, and leaves the
	// bot on a model it can start with
	if err := f.Set("magpie/relay/claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, bot} {
		c, raw := readHermes(t, p)
		if c.Providers[magpieID] != nil || c.Providers[hermesAnthropicID] != nil || hermesOurs(c.Model.Provider) {
			t.Fatalf("%s after disconnect:\n%s", p, raw)
		}
	}
}

// A config magpie connected before: the Claude model it set under "magpie"
// goes under "magpie-anthropic" on the next sync (an update), with the
// entry put in beside magpie's, and back when the model is no longer
// served on Messages.
func TestHermesSyncMovesClaudeOntoMessages(t *testing.T) {
	home, path := hermesAnthropicHome(t)
	gw := gateway.URL()
	os.WriteFile(path, []byte("model:\n  default: relay/claude-opus-5-5\n  provider: magpie\nproviders:\n  magpie:\n    name: magpie\n    api_key: "+keyAt(gw)+"\n    api_mode: chat_completions\n    base_url: "+gw+"/v1\n    extra_headers:\n      User-Agent: hermes-agent\n    models:\n      - relay/claude-opus-5-5\n      - deepseek/pro\n"), 0o644)
	setUpInMagpie("hermes")
	a := hermes(home)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	c, raw := readHermes(t, path)
	if c.Model.Provider != hermesAnthropicID || c.Providers[hermesAnthropicID] == nil || c.Providers[hermesAnthropicID].APIMode != "anthropic_messages" ||
		slices.Contains(c.Providers[magpieID].Models, "relay/claude-opus-5-5") {
		t.Fatalf("after sync:\n%s", raw)
	}
	// the relay now serves it on chat only: back under "magpie", and the
	// entry with no models goes
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "k", Models: []string{"claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if c, raw = readHermes(t, path); c.Model.Provider != magpieID || c.Providers[hermesAnthropicID] != nil || !slices.Contains(c.Providers[magpieID].Models, "relay/claude-opus-5-5") {
		t.Fatalf("after the relay went chat:\n%s", raw)
	}
}

// cherish_onlyy (#5315) put api_mode: anthropic_messages and the gateway's
// root into magpie's entry by hand, and the next update or connect made it
// chat_completions at /v1 again. An API the user gives one of magpie's
// entries is theirs: it stays through sync and a pick, its address
// follows it, and that entry then serves every model, as they set it.
func TestHermesKeepsTheUsersAPIMode(t *testing.T) {
	gw := gateway.URL()
	for _, c := range []struct{ name, entry, mode string }{
		{"api_mode", "    api_mode: anthropic_messages\n    base_url: " + gw + "\n", "anthropic_messages"},
		{"transport, an alias", "    transport: anthropic\n    base_url: " + gw + "/v1\n", "anthropic_messages"},
		{"responses", "    api_mode: codex_responses\n    base_url: " + gw + "/v1\n", "codex_responses"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, path := hermesAnthropicHome(t)
			os.WriteFile(path, []byte("model:\n  default: relay/claude-opus-5-5\n  provider: magpie\nproviders:\n  magpie:\n    name: magpie\n    api_key: "+keyAt(gw)+"\n"+c.entry+"    models:\n      - relay/claude-opus-5-5\n"), 0o644)
			setUpInMagpie("hermes")
			a := hermes(home)
			check := func(when, model string) {
				t.Helper()
				cfg, raw := readHermes(t, path)
				mp := cfg.Providers[magpieID]
				if mp == nil || cfg.Model.Provider != magpieID || cfg.Model.Default != model || cfg.Providers[hermesAnthropicID] != nil {
					t.Fatalf("%s:\n%s", when, raw)
				}
				if mode, mine := hermesModeIn(path, magpieID); !mine || mode != c.mode || mp.BaseURL != hermesBase(gw, c.mode) ||
					!slices.Contains(mp.Models, "relay/claude-opus-5-5") || !slices.Contains(mp.Models, "deepseek/pro") {
					t.Fatalf("%s: the user's API mode not kept:\n%s", when, raw)
				}
				if a.Check() != "" {
					t.Fatalf("%s: check %q", when, a.Check())
				}
			}
			for range 2 {
				if err := a.Sync(); err != nil {
					t.Fatal(err)
				}
			}
			check("sync", "relay/claude-opus-5-5")
			// connecting again (a pick on the Agents page) keeps it too
			if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
				t.Fatal(err)
			}
			check("pick", "deepseek/pro")
			if err := a.Field("model").Set("magpie/relay/claude-opus-5-5"); err != nil {
				t.Fatal(err)
			}
			check("pick claude", "relay/claude-opus-5-5")
		})
	}
	// chat_completions spelled another way is magpie's own, not a choice
	home, path := hermesAnthropicHome(t)
	os.WriteFile(path, []byte("model:\n  default: relay/claude-opus-5-5\n  provider: magpie\nproviders:\n  magpie:\n    api_mode: openai\n    base_url: "+gw+"/v1\n"), 0o644)
	setUpInMagpie("hermes")
	if err := hermes(home).Sync(); err != nil {
		t.Fatal(err)
	}
	if c, raw := readHermes(t, path); c.Model.Provider != hermesAnthropicID {
		t.Fatalf("openai alias:\n%s", raw)
	}
}
