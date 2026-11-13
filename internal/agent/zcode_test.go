package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func TestZCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".zcode", "v2", "config.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"provider":{"builtin:bigmodel":{"name":"Bigmodel","kind":"anthropic","enabled":true}}}`), 0o644)
	read := func() map[string]any {
		var c struct {
			Provider map[string]map[string]any `json:"provider"`
		}
		b, _ := os.ReadFile(path)
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		if c.Provider["builtin:bigmodel"] == nil {
			t.Fatalf("ZCode's own provider went: %s", b)
		}
		return c.Provider["magpie"]
	}

	a := zcode(home)
	if !a.Detected() {
		t.Fatal("not detected")
	}
	f := a.Field("provider")
	if f.Get() != "" {
		t.Fatalf("get: %q", f.Get())
	}
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	m := read()
	opts, _ := m["options"].(map[string]any)
	models, _ := m["models"].(map[string]any)
	pro, _ := models["deepseek/pro"].(map[string]any)
	if m["kind"] != "anthropic" || m["enabled"] != true || m["source"] != "custom" || opts["apiKey"] != "magpie" ||
		opts["baseURL"] == "" || pro == nil || pro["limit"] == nil || pro["modalities"] == nil {
		t.Fatalf("magpie provider: %v", m)
	}
	if f.Get() != "magpie" {
		t.Fatalf("get: %q", f.Get())
	}
	// ZCode 3.14 reads provider_config.json
	rules := filepath.Join(home, ".zcode", "v2", "provider_config.json")
	os.WriteFile(rules, []byte(`{"schemaVersion":1,"config":{"providerConfigRules":{"providerRules":[{"providerId":"mine","config":{}}]},"modelConfigRules":{"providerModelRules":[],"manualProviderModelRules":[{"providerId":"magpie","modelId":"deepseek/pro","config":{"enabled":true}}]}},"other":1}`), 0o600)
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	type rulesDoc struct {
		Other  int `json:"other"`
		Config struct {
			ProviderConfigRules struct {
				ProviderRules []map[string]any `json:"providerRules"`
			} `json:"providerConfigRules"`
			ModelConfigRules struct {
				ProviderModelRules       []map[string]any `json:"providerModelRules"`
				ManualProviderModelRules []map[string]any `json:"manualProviderModelRules"`
			} `json:"modelConfigRules"`
		} `json:"config"`
	}
	readRules := func() rulesDoc {
		var d rulesDoc
		b, _ := os.ReadFile(rules)
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		if d.Other != 1 || len(d.Config.ProviderConfigRules.ProviderRules) == 0 || d.Config.ProviderConfigRules.ProviderRules[0]["providerId"] != "mine" {
			t.Fatalf("ZCode's own rules went: %s", b)
		}
		return d
	}
	d := readRules()
	pr := d.Config.ProviderConfigRules.ProviderRules
	if len(pr) != 2 {
		t.Fatalf("provider rules: %v", pr)
	}
	cfg, _ := pr[1]["config"].(map[string]any)
	access, _ := cfg["access"].(map[string]any)
	api, _ := cfg["api"].(map[string]any)
	if pr[1]["providerId"] != "magpie" || pr[1]["enabled"] != true || cfg["group"] != "standard-personal" ||
		access["type"] != "api-key" || access["apiKey"] != "magpie" || api["type"] != "anthropic-messages" || api["baseUrl"] == "" ||
		len(cfg["personalModelIds"].([]any)) != 1 {
		t.Fatalf("magpie rule: %v", pr[1])
	}
	// a model set by hand in ZCode keeps its manual rule, and gets no second one
	if len(d.Config.ModelConfigRules.ManualProviderModelRules) != 1 || len(d.Config.ModelConfigRules.ProviderModelRules) != 0 {
		t.Fatalf("model rules: %+v", d.Config.ModelConfigRules)
	}

	// a provider added later reaches ZCode's picker
	if err := provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Chat: "https://api.moonshot.cn/v1", Key: "k", Models: []string{"k2"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if models, _ := read()["models"].(map[string]any); models["kimi/k2"] == nil {
		t.Fatalf("not synced: %v", models)
	}
	if d := readRules(); len(d.Config.ProviderConfigRules.ProviderRules[1]["config"].(map[string]any)["personalModelIds"].([]any)) != 2 ||
		len(d.Config.ModelConfigRules.ProviderModelRules) != 1 || d.Config.ModelConfigRules.ProviderModelRules[0]["modelId"] != "kimi/k2" {
		t.Fatalf("rules not synced: %+v", d.Config)
	}

	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if read() != nil || f.Get() != "" {
		t.Fatal("magpie provider left behind")
	}
	if d := readRules(); len(d.Config.ProviderConfigRules.ProviderRules) != 1 || len(d.Config.ModelConfigRules.ManualProviderModelRules) != 0 {
		t.Fatalf("magpie rules left behind: %+v", d.Config)
	}
	// nothing to sync into a config that has no magpie provider
	if err := a.Sync(); err != nil || read() != nil {
		t.Fatal("sync added the provider")
	}
}

// What the user did in ZCode stays: magpie turned off stays off, in both
// files; its rule stays where it is among theirs; and a rule they removed
// isn't put back when magpie syncs.
func TestZCodeKeepsUserChoices(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".zcode", "v2", "config.json")
	rules := filepath.Join(home, ".zcode", "v2", "provider_config.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"provider":{"magpie":{"name":"magpie","kind":"anthropic","enabled":false}}}`), 0o644)
	os.WriteFile(rules, []byte(`{"schemaVersion":1,"config":{"providerConfigRules":{"providerRules":[
	  {"providerId":"a","config":{}},{"providerId":"magpie","enabled":false,"config":{}},{"providerId":"b","config":{}}]}}}`), 0o600)
	a := zcode(home)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	ids := func() (out []string, on []any) {
		var d struct {
			Config struct {
				ProviderConfigRules struct {
					ProviderRules []map[string]any `json:"providerRules"`
				} `json:"providerConfigRules"`
			} `json:"config"`
		}
		b, _ := os.ReadFile(rules)
		json.Unmarshal(b, &d)
		for _, r := range d.Config.ProviderConfigRules.ProviderRules {
			out = append(out, r["providerId"].(string))
			on = append(on, r["enabled"])
		}
		return out, on
	}
	if got, on := ids(); len(got) != 3 || got[1] != "magpie" || on[1] != false {
		t.Fatalf("rules %v %v", got, on)
	}
	var c struct {
		Provider map[string]map[string]any `json:"provider"`
	}
	b, _ := os.ReadFile(path)
	json.Unmarshal(b, &c)
	if c.Provider["magpie"]["enabled"] != false || c.Provider["magpie"]["models"] == nil {
		t.Fatalf("config.json: %s", b)
	}

	// removed in ZCode: not put back
	os.WriteFile(rules, []byte(`{"schemaVersion":1,"config":{"providerConfigRules":{"providerRules":[{"providerId":"a","config":{}}]}}}`), 0o600)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if got, _ := ids(); len(got) != 1 {
		t.Fatalf("magpie put back: %v", got)
	}
}

// ZCode is told how long a reply may be and which reasoning levels a model
// takes, in both files; without them its model dialog shows 32000 tokens
// and thinking only on or off. A vendor's output that is really its window
// is capped, and a model whose output and levels aren't known gets neither.
func TestZCodeOutputAndReasoning(t *testing.T) {
	home := syncHome(t)
	os.WriteFile(catalog.CachePath(), []byte(`{"zai":{"models":{"glm-4.6":{"id":"glm-4.6","name":"GLM-4.6","limit":{"context":204800,"output":500000},
	  "reasoning_options":[{"type":"effort","values":["none","low","high","max"]}]}}}}`), 0o644)
	catalog.Reset()
	path := filepath.Join(home, ".zcode", "v2", "config.json")
	rules := filepath.Join(home, ".zcode", "v2", "provider_config.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"provider":{"builtin:zai":{"models":{"GLM-5-Turbo":{"reasoning":{"enabled":true,"variants":["enabled","off"],"defaultVariant":"enabled"},"limit":{"context":200000,"output":64000}}}}}}`), 0o644)
	os.WriteFile(rules, []byte(`{"schemaVersion":1,"config":{"providerConfigRules":{"providerRules":[]},"modelConfigRules":{"providerModelRules":[{"providerId":"mine","modelId":"x","config":{"optionSpecs":{"maxOutputTokens":{"max":7}}}}],"manualProviderModelRules":[]}}}`), 0o600)
	if err := zcode(home).Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	for _, want := range []string{
		`"limit":{"context":204800,"output":128000}`,
		// the reasoning keys ZCode reads (#3852): levels and defaultLevel,
		// with thinking sent per level, at the level's budget (a Claude
		// before 4.6 refuses "enabled" without one), and off for the level
		// named disabled
		`"reasoning":{"defaultLevel":"high","enabled":true,"levels":["disabled","low","high","max"],"providerOptionsByLevel":{"disabled":{"thinking":{"type":"disabled"}},"high":{"thinking":{"budget_tokens":24000,"type":"enabled"}},"low":{"thinking":{"budget_tokens":4096,"type":"enabled"}},"max":{"thinking":{"budget_tokens":32000,"type":"enabled"}}}}`,
		`"GLM-5-Turbo":{"reasoning":{"enabled":true,"variants":["enabled","off"],"defaultVariant":"enabled"},"limit":{"context":200000,"output":64000}}`,
	} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("config.json lacks %s: %s", want, b)
		}
	}
	b, _ = os.ReadFile(rules)
	for _, want := range []string{
		`"optionSpecs":{"maxOutputTokens":{"max":128000},"reasoningLevel":{"values":["disabled","low","high","max"]}}`,
		`{"config":{"optionSpecs":{"maxOutputTokens":{"max":7}}},"modelId":"x","providerId":"mine"}`,
	} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("provider_config.json lacks %s: %s", want, b)
		}
	}

	os.WriteFile(catalog.CachePath(), []byte(`{"zai":{"models":{"glm-4.6":{"id":"glm-4.6","name":"GLM-4.6","limit":{"context":204800}}}}}`), 0o644)
	catalog.Reset()
	if err := zcode(home).Sync(); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	c, _ := os.ReadFile(rules)
	if strings.Contains(string(b), "128000") || strings.Contains(string(b), `"disabled"`) ||
		strings.Contains(string(c), "128000") || strings.Contains(string(c), "reasoningLevel") {
		t.Fatalf("unknown output or levels written:\n%s\n%s", b, c)
	}
}

func TestZCodeDefaultLevel(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"low", "medium", "high"}, "medium"},
		{[]string{"disabled", "low", "high", "max"}, "high"},
		{[]string{"low", "high"}, "high"},
		{[]string{"disabled"}, "disabled"},
	} {
		if got := zcodeDefaultLevel(c.in); got != c.want {
			t.Errorf("%v: %q, want %q", c.in, got, c.want)
		}
	}
}

// A provider the user pointed at a magpie on another machine (a NAS) keeps
// that address and key through a sync, in both of ZCode's files; one on
// loopback follows the gateway, and connecting it in magpie points it here.
func TestZCodeKeepsRemoteAddress(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	a := zcode(home)
	if err := a.Fields[0].Set(magpieID); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".zcode", "v2", "config.json")
	rules := filepath.Join(home, ".zcode", "v2", "provider_config.json")
	addr := func() (string, string, string, string) {
		t.Helper()
		var c struct {
			Provider map[string]struct {
				Options map[string]string `json:"options"`
			} `json:"provider"`
		}
		b, _ := os.ReadFile(path)
		json.Unmarshal(b, &c)
		var r struct {
			Config struct {
				ProviderConfigRules struct {
					ProviderRules []struct {
						ProviderID string `json:"providerId"`
						Config     struct {
							Access map[string]string `json:"access"`
							API    map[string]string `json:"api"`
						} `json:"config"`
					} `json:"providerRules"`
				} `json:"providerConfigRules"`
			} `json:"config"`
		}
		b, _ = os.ReadFile(rules)
		json.Unmarshal(b, &r)
		var base, key string
		for _, p := range r.Config.ProviderConfigRules.ProviderRules {
			if p.ProviderID == magpieID {
				base, key = p.Config.API["baseUrl"], p.Config.Access["apiKey"]
			}
		}
		o := c.Provider[magpieID].Options
		return o["baseURL"], o["apiKey"], base, key
	}

	// as the user would in ZCode: both files, from what they hold now
	point := func(base, key string) {
		t.Helper()
		was, wasKey, _, _ := addr()
		b, _ := os.ReadFile(path)
		s := strings.ReplaceAll(string(b), was, base)
		os.WriteFile(path, []byte(strings.ReplaceAll(s, `"apiKey": "`+wasKey+`"`, `"apiKey": "`+key+`"`)), 0o644)
		b, _ = os.ReadFile(rules)
		s = strings.ReplaceAll(string(b), was, base)
		os.WriteFile(rules, []byte(strings.ReplaceAll(s, `"apiKey":"`+wasKey+`"`, `"apiKey":"`+key+`"`)), 0o600)
		if b, k, rb, rk := addr(); b != base || k != key || rb != base || rk != key {
			t.Fatalf("pointing at %s: config %s %s, rules %s %s", base, b, k, rb, rk)
		}
	}
	point("http://192.168.1.20:3425", "nas-key")
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if b, k, rb, rk := addr(); b != "http://192.168.1.20:3425" || k != "nas-key" || rb != b || rk != k {
		t.Fatalf("a sync moved the NAS address: config %s %s, rules %s %s", b, k, rb, rk)
	}
	if b, _ := os.ReadFile(rules); !strings.Contains(string(b), "flash") {
		t.Fatalf("the sync didn't bring the new model: %s", b)
	}

	point("http://localhost:4000", "old")
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if b, k, rb, rk := addr(); b != gateway.URL() || k != gateway.Token || rb != b || rk != k {
		t.Fatalf("a loopback address didn't follow the gateway: config %s %s, rules %s %s", b, k, rb, rk)
	}

	point("https://nas.example:8443", "nas-key")
	if err := a.Fields[0].Set(magpieID); err != nil {
		t.Fatal(err)
	}
	if b, k, rb, rk := addr(); b != gateway.URL() || k != gateway.Token || rb != b || rk != k {
		t.Fatalf("connecting in magpie kept the NAS address: config %s %s, rules %s %s", b, k, rb, rk)
	}

	// ZCode's own settings change one file only (Discord, 悠悠哥: the NAS
	// address kept going back to this machine's magpie): a sync carries the
	// NAS address to the other one rather than taking it back
	only := func(file, base, key string) {
		t.Helper()
		was, wasKey, _, _ := addr()
		b, _ := os.ReadFile(file)
		s := strings.ReplaceAll(string(b), was, base)
		s = strings.ReplaceAll(s, `"apiKey": "`+wasKey+`"`, `"apiKey": "`+key+`"`)
		os.WriteFile(file, []byte(strings.ReplaceAll(s, `"apiKey":"`+wasKey+`"`, `"apiKey":"`+key+`"`)), 0o644)
	}
	for _, file := range []string{rules, path} {
		if err := a.Fields[0].Set(magpieID); err != nil {
			t.Fatal(err)
		}
		only(file, "http://10.0.0.8:3425", "nas-key")
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
		if b, k, rb, rk := addr(); b != "http://10.0.0.8:3425" || k != "nas-key" || rb != b || rk != k {
			t.Fatalf("a NAS address in %s alone was taken back: config %s %s, rules %s %s", filepath.Base(file), b, k, rb, rk)
		}
	}

	for base, want := range map[string]bool{"http://192.168.1.20:3425": true, "https://nas.lan": true, "http://127.0.0.1:3425": false,
		"http://localhost:3425": false, "http://[::1]:3425": false, "http://0.0.0.0:3425": false, "": false, "nas:3425": false} {
		if onAnotherMachine(base) != want {
			t.Errorf("onAnotherMachine(%q) = %v, want %v", base, !want, want)
		}
	}
}

// The API format the user picks for magpie's provider in ZCode's settings
// stays theirs: magpie answers all three, and a sync put Anthropic Messages
// back on every start (杰多夫 on Discord). A change made in either file is
// carried to the other; a provider magpie adds new starts on Anthropic.
func TestZCodeKeepsUserFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".zcode", "v2")
	path, rules := filepath.Join(dir, "config.json"), filepath.Join(dir, "provider_config.json")
	os.MkdirAll(dir, 0o755)
	// what ZCode's own settings do: write the file, after magpie's write
	edited := func(p string, change func(doc map[string]any)) {
		b, _ := os.ReadFile(p)
		doc := map[string]any{}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		change(doc)
		b, _ = json.Marshal(doc)
		os.WriteFile(p, b, 0o600)
		later := time.Now().Add(time.Minute)
		os.Chtimes(p, later, later)
	}
	rule := func(doc map[string]any) map[string]any {
		for _, r := range doc["config"].(map[string]any)["providerConfigRules"].(map[string]any)["providerRules"].([]any) {
			if r := r.(map[string]any); r["providerId"] == "magpie" {
				return r["config"].(map[string]any)["api"].(map[string]any)
			}
		}
		t.Fatal("no magpie rule")
		return nil
	}
	formats := func() (kind, api string) {
		k, _ := edit.GetJSON(path, "provider.magpie.kind")
		b, _ := os.ReadFile(rules)
		doc := map[string]any{}
		json.Unmarshal(b, &doc)
		r := rule(doc)
		if r["baseUrl"] == "" {
			t.Fatalf("no address: %s", b)
		}
		api, _ = r["type"].(string)
		return k, api
	}
	a := zcode(home)
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if k, api := formats(); k != "anthropic" || api != "anthropic-messages" {
		t.Fatalf("new provider: %q %q", k, api)
	}

	// ZCode 3.14 on writes provider_config.json
	for _, c := range []struct{ api, kind string }{
		{"openai-chat-completions", "openai-compatible"},
		{"openai-responses", "openai"},
		{"anthropic-messages", "anthropic"},
	} {
		edited(rules, func(doc map[string]any) { rule(doc)["type"] = c.api })
		for range 2 { // and on the next start
			if err := a.Sync(); err != nil {
				t.Fatal(err)
			}
			if k, api := formats(); k != c.kind || api != c.api {
				t.Fatalf("picked %s: kind %q, api %q", c.api, k, api)
			}
		}
	}

	// an older ZCode writes config.json
	edited(path, func(doc map[string]any) {
		doc["provider"].(map[string]any)["magpie"].(map[string]any)["kind"] = "openai-compatible"
	})
	later := time.Now().Add(2 * time.Minute) // past rules' edit above
	os.Chtimes(path, later, later)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if k, api := formats(); k != "openai-compatible" || api != "openai-chat-completions" {
		t.Fatalf("config.json's kind: %q %q", k, api)
	}

	// a format ZCode doesn't have isn't kept
	edited(rules, func(doc map[string]any) { rule(doc)["type"] = "grpc" })
	edited(path, func(doc map[string]any) { doc["provider"].(map[string]any)["magpie"].(map[string]any)["kind"] = "grpc" })
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if k, api := formats(); k != "anthropic" || api != "anthropic-messages" {
		t.Fatalf("unknown format: %q %q", k, api)
	}
}

// A ZCode whose data the user moved in its settings (数据存储路径) reads its
// providers from the new folder: magpie's provider goes there, and the home
// folder's .zcode, where only setting.json is left, gets none (flower on
// Discord: magpie said connected, and ZCode's settings had no magpie).
func TestZCodeMovedDataFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(t.TempDir(), "ZCodeData")
	homeV2, movedV2 := filepath.Join(home, ".zcode", "v2"), filepath.Join(moved, ".zcode", "v2")
	os.MkdirAll(homeV2, 0o755)
	os.MkdirAll(movedV2, 0o755)
	// what ZCode writes when the folder is moved: the choice stays at home,
	// the data (an empty provider list among it) is copied over
	setting, _ := json.Marshal(map[string]any{"locale": "zh-CN", "dataBaseDir": moved})
	os.WriteFile(filepath.Join(homeV2, "setting.json"), setting, 0o644)
	empty := `{"schemaVersion":1,"config":{"providerConfigRules":{"providerRules":[]},"modelConfigRules":{"providerModelRules":[],"manualProviderModelRules":[]}}}`
	os.WriteFile(filepath.Join(movedV2, "provider_config.json"), []byte(empty), 0o600)

	a := zcode(home)
	f := a.Field("provider")
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if !zcodeRuled(filepath.Join(movedV2, "provider_config.json")) {
		b, _ := os.ReadFile(filepath.Join(movedV2, "provider_config.json"))
		t.Fatalf("no magpie in the moved folder's provider_config.json: %s", b)
	}
	if _, ok := edit.GetJSON(filepath.Join(movedV2, "config.json"), "provider.magpie"); !ok {
		t.Fatal("no magpie in the moved folder's config.json")
	}
	for _, n := range []string{"provider_config.json", "config.json"} {
		if _, err := os.Stat(filepath.Join(homeV2, n)); err == nil {
			t.Fatalf("%s written in the home folder ZCode no longer reads", n)
		}
	}
	if f.Get() != "magpie" {
		t.Fatalf("get: %q", f.Get())
	}

	// a relative or empty dataBaseDir is not a folder ZCode moved to
	for _, d := range []string{"", "  ", "ZCodeData"} {
		setting, _ := json.Marshal(map[string]any{"dataBaseDir": d})
		os.WriteFile(filepath.Join(homeV2, "setting.json"), setting, 0o644)
		if got := zcode(home).Dir; got != filepath.Join(home, ".zcode") {
			t.Fatalf("dataBaseDir %q: %s", d, got)
		}
	}
}

// What the user set on magpie's provider rule in ZCode's provider_config.json
// stays through every sync and pick (#2030's siblings): its name, logo,
// visibility, a header, the key's management link. The address, key, format
// and model lists stay magpie's.
func TestZCodeKeepsTheUsersSettings(t *testing.T) {
	home := syncHome(t)
	rules := filepath.Join(home, ".zcode", "v2", "provider_config.json")
	os.MkdirAll(filepath.Dir(rules), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(rules), "config.json"), []byte(`{}`), 0o644)
	a := zcode(home)
	f := a.Field("provider")
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	type rule = map[string]any
	load := func() (map[string]any, rule) {
		var d map[string]any
		raw := readFile(rules)
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			t.Fatalf("%v\n%s", err, raw)
		}
		for _, r := range zcodeList(zcodeObj(zcodeObj(d, "config"), "providerConfigRules"), "providerRules") {
			if m := r.(map[string]any); m["providerId"] == magpieID {
				return d, m
			}
		}
		t.Fatalf("no rule:\n%s", raw)
		return nil, nil
	}
	d, r := load()
	cfg := r["config"].(map[string]any)
	r["providerName"] = "My GW"
	cfg["logo"] = map[string]any{"type": "builtin", "key": "openai"}
	cfg["visibility"] = "visible"
	cfg["api"].(map[string]any)["headers"] = map[string]any{"X-Mine": "1"}
	cfg["api"].(map[string]any)["baseUrl"] = "http://127.0.0.1:1/v1"
	cfg["access"].(map[string]any)["apiKeyManagementUrl"] = "https://x/keys"
	cfg["personalModelIds"] = []any{"gone/old"}
	b, _ := json.Marshal(d)
	os.WriteFile(rules, b, 0o600)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	check := func(when string) {
		t.Helper()
		_, r := load()
		cfg := r["config"].(map[string]any)
		api, access := cfg["api"].(map[string]any), cfg["access"].(map[string]any)
		headers, _ := api["headers"].(map[string]any)
		logo, _ := cfg["logo"].(map[string]any)
		if r["providerName"] != "My GW" || logo["key"] != "openai" || cfg["visibility"] != "visible" || headers["X-Mine"] != "1" ||
			access["apiKeyManagementUrl"] != "https://x/keys" {
			t.Errorf("%s: the user's settings not kept:\n%s", when, readFile(rules))
		}
		ids, _ := cfg["personalModelIds"].([]any)
		if api["baseUrl"] == "http://127.0.0.1:1/v1" || access["apiKey"] == "" || slices.Contains(ids, any("gone/old")) || !slices.Contains(ids, any("deepseek/pro")) {
			t.Errorf("%s: magpie's own parts not written:\n%s", when, readFile(rules))
		}
	}
	check("sync")
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	check("pick")
}

// ZCode reads $HOME before os.homedir() on every OS, so on a Windows where
// the user set HOME (for Git Bash, MSYS2, Emacs) its providers, settings
// and moved data folder are under %HOME%\.zcode, not %USERPROFILE%\.zcode,
// Go's home. magpie wrote its provider under USERPROFILE: connected, no
// error, and no magpie in ZCode (Gearclickist on Discord).
func TestZCodeOnWindowsUsesTheHomeZCodeReads(t *testing.T) {
	profile := syncHome(t)
	old := zcodeOS
	zcodeOS = "windows"
	t.Cleanup(func() { zcodeOS = old })
	set := t.TempDir()
	t.Setenv("HOME", set)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(set, ".zcode")
	a := zcode(profile)
	if a.Dir != want || a.Path != filepath.Join(want, "v2", "config.json") {
		t.Fatalf("ZCode's folder on Windows with HOME set: %s, %s; want %s", a.Dir, a.Path, want)
	}
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if !zcodeRuled(filepath.Join(want, "v2", "provider_config.json")) {
		t.Fatalf("no magpie in %s:\n%s", want, readFile(filepath.Join(want, "v2", "provider_config.json")))
	}
	if _, ok := edit.GetJSON(filepath.Join(want, "v2", "config.json"), "provider.magpie"); !ok {
		t.Fatal("no magpie in config.json under HOME")
	}
	if _, err := os.Stat(filepath.Join(profile, ".zcode")); err == nil {
		t.Fatal("written under USERPROFILE, which ZCode doesn't read while HOME is set")
	}

	// the data folder moved in ZCode's settings is read from the setting.json
	// under HOME, where ZCode keeps it
	moved := t.TempDir()
	setting, _ := json.Marshal(map[string]any{"dataBaseDir": moved})
	os.WriteFile(filepath.Join(want, "v2", "setting.json"), setting, 0o644)
	if got := zcode(profile).Dir; got != filepath.Join(moved, ".zcode") {
		t.Fatalf("moved data folder under HOME's settings: %s", got)
	}
	os.Remove(filepath.Join(want, "v2", "setting.json"))

	// no HOME, or one that isn't rooted: USERPROFILE, as ZCode's os.homedir()
	for _, h := range []string{"", "  ", "relative"} {
		t.Setenv("HOME", h)
		if got := zcode(profile).Dir; got != filepath.Join(profile, ".zcode") {
			t.Fatalf("HOME %q: %s", h, got)
		}
	}
	// a folder that isn't the user's own home (a test's, a WSL distro's) is
	// ZCode's as given
	t.Setenv("HOME", set)
	other := t.TempDir()
	if got := zcode(other).Dir; got != filepath.Join(other, ".zcode") {
		t.Fatalf("a given folder that isn't USERPROFILE: %s", got)
	}
	// elsewhere Go's home is HOME already: the folder given stays
	zcodeOS = "darwin"
	if got := zcode(profile).Dir; got != filepath.Join(profile, ".zcode") {
		t.Fatalf("darwin: %s", got)
	}
}

// A model whose source lists video input is one ZCode attaches a video to
// (Flandern1211 on X, #5098): both of ZCode's files say it takes video while
// the provider is on Messages or Chat, which the gateway carries a video on,
// and neither does on Responses, where ZCode refuses one. A model without
// video says nothing of it, and no PDF is offered while a translated
// request loses one (#5452).
func TestZCodeToldOfVideoInput(t *testing.T) {
	home := syncHome(t)
	base := "https://example.test/v1"
	if err := provider.Save(provider.Provider{ID: "media", Name: "Media", Chat: base, Key: "k", Models: []string{"gemini", "plain"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("media", base, []catalog.Model{
		{ID: "gemini", Images: true, InputModalities: []string{"text", "image", "video", "audio", "pdf"}},
		{ID: "plain", Images: true, InputModalities: []string{"text", "image"}},
	}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".zcode", "v2")
	path, rules := filepath.Join(dir, "config.json"), filepath.Join(dir, "provider_config.json")
	os.MkdirAll(dir, 0o755)
	a := zcode(home)
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	read := func() (in map[string][]string, format map[string]map[string]any, api string) {
		t.Helper()
		var c struct {
			Provider map[string]struct {
				Models map[string]struct {
					Modalities struct {
						Input []string `json:"input"`
					} `json:"modalities"`
				} `json:"models"`
			} `json:"provider"`
		}
		if err := json.Unmarshal([]byte(readFile(path)), &c); err != nil {
			t.Fatal(err)
		}
		in = map[string][]string{}
		for id, m := range c.Provider["magpie"].Models {
			in[id] = m.Modalities.Input
		}
		var d struct {
			Config struct {
				ProviderConfigRules struct {
					ProviderRules []struct {
						ProviderID string `json:"providerId"`
						Config     struct {
							API struct {
								Type string `json:"type"`
							} `json:"api"`
						} `json:"config"`
					} `json:"providerRules"`
				} `json:"providerConfigRules"`
				ModelConfigRules struct {
					ProviderModelRules []struct {
						ModelID string `json:"modelId"`
						Config  struct {
							Properties struct {
								InputFormat map[string]any `json:"inputFormat"`
							} `json:"properties"`
						} `json:"config"`
					} `json:"providerModelRules"`
				} `json:"modelConfigRules"`
			} `json:"config"`
		}
		if err := json.Unmarshal([]byte(readFile(rules)), &d); err != nil {
			t.Fatal(err)
		}
		format = map[string]map[string]any{}
		for _, r := range d.Config.ModelConfigRules.ProviderModelRules {
			format[r.ModelID] = r.Config.Properties.InputFormat
		}
		for _, r := range d.Config.ProviderConfigRules.ProviderRules {
			if r.ProviderID == "magpie" {
				api = r.Config.API.Type
			}
		}
		return in, format, api
	}
	for _, api := range []string{"anthropic-messages", "openai-chat-completions", "openai-responses"} {
		if api != "anthropic-messages" {
			// the format picked in ZCode's settings, kept by a sync
			doc := map[string]any{}
			if err := json.Unmarshal([]byte(readFile(rules)), &doc); err != nil {
				t.Fatal(err)
			}
			for _, r := range doc["config"].(map[string]any)["providerConfigRules"].(map[string]any)["providerRules"].([]any) {
				if r := r.(map[string]any); r["providerId"] == "magpie" {
					r["config"].(map[string]any)["api"].(map[string]any)["type"] = api
				}
			}
			b, _ := json.Marshal(doc)
			os.WriteFile(rules, b, 0o600)
			later := time.Now().Add(time.Minute)
			os.Chtimes(rules, later, later)
			if err := a.Sync(); err != nil {
				t.Fatal(err)
			}
		}
		in, format, got := read()
		if got != api {
			t.Fatalf("format %q, want %q:\n%s", got, api, readFile(rules))
		}
		video := api != "openai-responses"
		want := []string{"text", "image"}
		if video {
			want = append(want, "video")
		}
		if !slices.Equal(in["media/gemini"], want) {
			t.Fatalf("%s: config.json's media/gemini takes %v, want %v", api, in["media/gemini"], want)
		}
		if !slices.Equal(in["media/plain"], []string{"text", "image"}) {
			t.Fatalf("%s: config.json's media/plain takes %v", api, in["media/plain"])
		}
		if f := format["media/gemini"]; f["supportsImage"] != true || (f["supportsVideo"] == true) != video || f["supportsPdf"] != nil || f["supportsAudio"] != nil {
			t.Fatalf("%s: media/gemini's inputFormat %v", api, f)
		}
		if f := format["media/plain"]; f["supportsImage"] != true || f["supportsVideo"] != nil {
			t.Fatalf("%s: media/plain's inputFormat %v", api, f)
		}
	}
}
