package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// outputHome is syncHome with a model models.dev says replies 384000
// tokens, as it says of deepseek-v4.1-flash, served by the relay.
func outputHome(t *testing.T) string {
	t.Helper()
	home := syncHome(t)
	listOutput(t, 384000)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-v4.1-flash"}}); err != nil {
		t.Fatal(err)
	}
	return home
}

// listOutput is models.dev saying the model replies n tokens.
func listOutput(t *testing.T, n int) {
	t.Helper()
	os.WriteFile(catalog.CachePath(), []byte(`{"deepseek":{"models":{"deepseek-v4.1-flash":{"id":"deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash","limit":{"context":1000000,"output":`+strconv.Itoa(n)+`}}}}}`), 0o644)
	catalog.Reset()
}

// sayagoodbyeplz on Discord: WorkBuddy's deepseek-v4.1-flash replies
// 128000 tokens, not the 384000 models.dev says, and a maxTokens set to
// 128000 by hand in Pi's models.json was written back to 384000 by the
// next sync. One typed there stays; a Max output set in magpie is written
// over it; and with neither, magpie's own follows models.dev.
func TestPiKeepsAMaxTokensTypedByHand(t *testing.T) {
	home := outputHome(t)
	piModels := filepath.Join(home, ".pi", "agent", "models.json")
	writeFile(t, filepath.Join(home, ".pi", "agent", "settings.json"), `{"defaultProvider":"magpie","defaultModel":"relay/deepseek-v4.1-flash"}`)
	writeFile(t, piModels, `{"providers":{"magpie":{"name":"magpie","models":[]}}}`)
	setUpInMagpie("pi")
	const at = "providers.magpie.models.0.maxTokens"
	got := func() string { v, _ := edit.GetJSON(piModels, at); return v }
	SyncCatalog()
	if got() != "384000" {
		t.Fatalf("magpie's maxTokens = %s, want models.dev's 384000\n%s", got(), readFile(piModels))
	}

	// models.dev changes, nothing typed: magpie's follows it
	listOutput(t, 300000)
	SyncCatalog()
	if got() != "300000" {
		t.Fatalf("after models.dev changed, maxTokens = %s, want 300000", got())
	}

	// typed by hand: it stays through syncs, and models.dev changing
	if err := edit.SetJSON(piModels, edit.KV{Path: at, Value: 128000}); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	if got() != "128000" {
		t.Fatalf("a maxTokens typed by hand was written over: %s", got())
	}
	listOutput(t, 384000)
	SyncCatalog()
	if got() != "128000" {
		t.Fatalf("a maxTokens typed by hand was written over when models.dev changed: %s", got())
	}

	// set in magpie: the user's word there is written over it
	if err := provider.SetModelOutput("relay/deepseek-v4.1-flash", 100000); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	if got() != "100000" {
		t.Fatalf("a Max output set in magpie didn't reach Pi: %s", got())
	}
	// and taken away, magpie's own again
	if err := provider.SetModelOutput("relay/deepseek-v4.1-flash", 0); err != nil {
		t.Fatal(err)
	}
	SyncCatalog()
	if got() != "384000" {
		t.Fatalf("with no Max output set, maxTokens = %s, want 384000", got())
	}
}

// The same for OpenCode's limit.output.
func TestOpenCodeKeepsALimitOutputTypedByHand(t *testing.T) {
	home := outputHome(t)
	cfg := filepath.Join(home, ".config", "opencode", "opencode.json")
	writeFile(t, cfg, `{"model":"magpie/relay/deepseek-v4.1-flash","provider":{"magpie":{"name":"magpie","npm":"@ai-sdk/openai-compatible","options":{"baseURL":"http://127.0.0.1:3425/v1","apiKey":"magpie"},"models":{}}}}`)
	oc := opencode(home, filepath.Join(home, ".config"))
	const at = "provider.magpie.models.relay/deepseek-v4\\.1-flash.limit.output"
	got := func() string { v, _ := edit.GetJSON(cfg, at); return v }
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	if got() != "384000" {
		t.Fatalf("magpie's limit.output = %q, want 384000\n%s", got(), readFile(cfg))
	}
	if err := edit.SetJSON(cfg, edit.KV{Path: at, Value: 128000}); err != nil {
		t.Fatal(err)
	}
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	if got() != "128000" {
		t.Fatalf("a limit.output typed by hand was written over: %s", got())
	}
	if err := provider.SetModelOutput("relay/deepseek-v4.1-flash", 100000); err != nil {
		t.Fatal(err)
	}
	if err := oc.Sync(); err != nil {
		t.Fatal(err)
	}
	if got() != "100000" {
		t.Fatalf("a Max output set in magpie didn't reach OpenCode: %s", got())
	}
}

// Flandern1211 on X: a max output edited by hand in dsh or ZCode was
// written back by the next sync. For each file magpie writes the models
// into: magpie's own value first, then one typed by hand stays through
// syncs and models.dev changing, a Max output set in magpie is written over
// it, and taken away, magpie's own follows models.dev again.
func checkOutputKept(t *testing.T, name string, wantFirst int, read func() string, typeIn func(n int), sync func()) {
	t.Helper()
	const model = "relay/deepseek-v4.1-flash"
	want := func(n int, why string) {
		t.Helper()
		if got := read(); got != strconv.Itoa(n) {
			t.Fatalf("%s: %s: output = %q, want %d", name, why, got, n)
		}
	}
	sync()
	want(wantFirst, "first sync")
	typeIn(64000)
	sync()
	want(64000, "typed by hand, synced")
	listOutput(t, 300000)
	sync()
	want(64000, "typed by hand, models.dev changed")
	if err := provider.SetModelOutput(model, 100000); err != nil {
		t.Fatal(err)
	}
	sync()
	want(100000, "Max output set in magpie")
	if err := provider.SetModelOutput(model, 0); err != nil {
		t.Fatal(err)
	}
	sync()
	if got := read(); got == "100000" || got == "64000" {
		t.Fatalf("%s: with no Max output set, output = %s, want models.dev's", name, got)
	}
}

func TestZCodeKeepsAMaxOutputTypedByHand(t *testing.T) {
	home := outputHome(t)
	a := zcode(home)
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(home, ".zcode", "v2", "config.json")
	// config.json's limit.output
	const at = "provider.magpie.models.relay/deepseek-v4\\.1-flash.limit.output"
	checkOutputKept(t, "zcode config.json", 128000,
		func() string { v, _ := edit.GetJSON(cfg, at); return v },
		func(n int) {
			if err := edit.SetJSON(cfg, edit.KV{Path: at, Value: n}); err != nil {
				t.Fatal(err)
			}
		},
		func() {
			if err := a.Sync(); err != nil {
				t.Fatal(err)
			}
		})
}

func TestZCodeRulesKeepAMaxOutputTypedByHand(t *testing.T) {
	home := outputHome(t)
	a := zcode(home)
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	rules := filepath.Join(home, ".zcode", "v2", "provider_config.json")
	read := func() string {
		var d struct {
			Config struct {
				Models struct {
					Rules []struct {
						Provider string `json:"providerId"`
						Config   struct {
							Specs struct {
								Out struct {
									Max int `json:"max"`
								} `json:"maxOutputTokens"`
							} `json:"optionSpecs"`
						} `json:"config"`
					} `json:"providerModelRules"`
				} `json:"modelConfigRules"`
			} `json:"config"`
		}
		_ = json.Unmarshal([]byte(readFile(rules)), &d)
		for _, r := range d.Config.Models.Rules {
			if r.Provider == "magpie" {
				return strconv.Itoa(r.Config.Specs.Out.Max)
			}
		}
		return "none"
	}
	checkOutputKept(t, "zcode provider_config.json", 128000, read,
		func(n int) {
			var d map[string]any
			_ = json.Unmarshal([]byte(readFile(rules)), &d)
			mcr := d["config"].(map[string]any)["modelConfigRules"].(map[string]any)
			for _, r := range mcr["providerModelRules"].([]any) {
				if m := r.(map[string]any); m["providerId"] == "magpie" {
					m["config"].(map[string]any)["optionSpecs"].(map[string]any)["maxOutputTokens"] = map[string]any{"max": n}
				}
			}
			b, _ := json.Marshal(d)
			writeFile(t, rules, string(b))
		},
		func() {
			if err := a.Sync(); err != nil {
				t.Fatal(err)
			}
		})
}

func TestDshKeepsAMaxTokensTypedByHand(t *testing.T) {
	home := outputHome(t)
	web := filepath.Join(home, ".dsh", "profiles", "web", "cordis.patch.yml")
	os.MkdirAll(filepath.Dir(web), 0o755)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"), 0o644)
	a := dsh(home)
	if err := a.Field("model").Set("magpie/relay/deepseek-v4.1-flash"); err != nil {
		t.Fatal(err)
	}
	path := web
	re := regexp.MustCompile(`(?m)^(\s+maxTokens: )(\d+)$`)
	checkOutputKept(t, "dsh", 384000,
		func() string {
			m := re.FindStringSubmatch(readFile(path))
			if m == nil {
				return "none:\n" + readFile(path)
			}
			return m[2]
		},
		func(n int) {
			writeFile(t, path, re.ReplaceAllString(readFile(path), "${1}"+strconv.Itoa(n)))
		},
		func() {
			if err := a.Sync(); err != nil {
				t.Fatal(err)
			}
		})
}

func TestWorkBuddyKeepsAMaxOutputTypedByHand(t *testing.T) {
	home := outputHome(t)
	path := filepath.Join(home, "models.json")
	re := regexp.MustCompile(`("maxOutputTokens": )(\d+)`)
	checkOutputKept(t, "workbuddy", 128000,
		func() string {
			m := re.FindStringSubmatch(readFile(path))
			if m == nil {
				return "none:\n" + readFile(path)
			}
			return m[2]
		},
		func(n int) {
			writeFile(t, path, re.ReplaceAllString(readFile(path), "${1}"+strconv.Itoa(n)))
		},
		func() {
			if err := workbuddyWrite(path, true); err != nil {
				t.Fatal(err)
			}
		})
}
