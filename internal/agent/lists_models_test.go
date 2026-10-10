package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// listsSandbox is a HOME of its own with one provider of two models.
func listsSandbox(t *testing.T) (home, cfg string) {
	t.Helper()
	home = t.TempDir()
	cfg = filepath.Join(home, ".config")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	return home, cfg
}

// An agent whose switch puts magpie's models in a picker of its own, with
// no field of its picking among them, lists only the ones picked for it on
// the Agents page, as every other agent does: ZCode was given every model,
// with no way to hide one (#1508, NovaTrailX). ListsModels is what gives
// its row that list.
func TestEveryPickerOfMagpiesModelsCanBeNarrowed(t *testing.T) {
	home, cfg := listsSandbox(t)
	var bare, menus []string
	for _, a := range builtins(home, cfg) {
		vals := a.Values()
		refs, magpie := false, false
		for _, f := range a.Fields {
			if f.Options == nil {
				continue
			}
			for _, o := range f.Options(vals) {
				refs = refs || o.Ref != ""
				magpie = magpie || o.Value == magpieID
			}
		}
		switch {
		// Claude Desktop's tier pickers list the catalog once it is
		// connected (desktopTierFields), and the sandbox has no Desktop
		case refs, a.ID == "claude-desktop":
		case a.ListsModels:
			menus = append(menus, a.ID)
		case magpie:
			bare = append(bare, a.ID)
		}
	}
	if len(bare) > 0 {
		t.Errorf("magpie's models are put in these agents' pickers with no way to pick which: %v", bare)
	}
	for _, id := range []string{"zcode", "pencil", "t3code", "workbuddy", "workbuddy-ai", copilotJBID, CursorLocalID} {
		if !slices.Contains(menus, id) {
			t.Errorf("%s's row has no model list to pick in (ListsModels): %v", id, menus)
		}
	}
}

// The models taken out of ZCode's list on the Agents page are out of both
// files it reads, config.json and provider_config.json, and come back when
// they are put back; what the user set by hand stays: their own providers,
// rules and keys, and their manual rule for a model of magpie's (#1508).
func TestZCodeListsOnlyThePicked(t *testing.T) {
	home, _ := listsSandbox(t)
	dir := filepath.Join(home, ".zcode", "v2")
	path, rules := filepath.Join(dir, "config.json"), filepath.Join(dir, "provider_config.json")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(path, []byte(`{"provider":{"builtin:bigmodel":{"name":"Bigmodel","kind":"anthropic","enabled":true},"mine":{"name":"Mine","kind":"openai","models":{"m1":{"name":"M1"}}}},"theme":"dark"}`), 0o644)
	os.WriteFile(rules, []byte(`{"schemaVersion":1,"config":{"providerConfigRules":{"providerRules":[{"providerId":"mine","enabled":true,"config":{"personalModelIds":["m1"]}}]},"modelConfigRules":{"providerModelRules":[{"providerId":"mine","modelId":"m1","config":{"properties":{"contextWindow":1000}}}],"manualProviderModelRules":[{"providerId":"magpie","modelId":"deepseek/pro","config":{"enabled":true,"properties":{"contextWindow":4242}}}]}},"ui":{"lang":"zh"}}`), 0o600)
	a := zcode(home)
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	type configDoc struct {
		Theme    string                    `json:"theme"`
		Provider map[string]map[string]any `json:"provider"`
	}
	readConfig := func() configDoc {
		var c configDoc
		b, _ := os.ReadFile(path)
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		if c.Theme != "dark" || c.Provider["builtin:bigmodel"] == nil || c.Provider["mine"]["models"].(map[string]any)["m1"] == nil {
			t.Fatalf("the user's own config went: %s", b)
		}
		return c
	}
	type rulesDoc struct {
		UI     map[string]any `json:"ui"`
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
		pr, mr := d.Config.ProviderConfigRules.ProviderRules, d.Config.ModelConfigRules
		if d.UI["lang"] != "zh" || len(pr) != 2 || pr[0]["providerId"] != "mine" ||
			!slices.ContainsFunc(mr.ProviderModelRules, func(r map[string]any) bool { return r["providerId"] == "mine" && r["modelId"] == "m1" }) ||
			len(mr.ManualProviderModelRules) != 1 || mr.ManualProviderModelRules[0]["modelId"] != "deepseek/pro" {
			t.Fatalf("the user's own rules went: %s", b)
		}
		return d
	}
	listed := func() (config, personal, ruled []string) {
		for id := range readConfig().Provider["magpie"]["models"].(map[string]any) {
			config = append(config, id)
		}
		d := readRules()
		for _, id := range d.Config.ProviderConfigRules.ProviderRules[1]["config"].(map[string]any)["personalModelIds"].([]any) {
			personal = append(personal, id.(string))
		}
		for _, r := range d.Config.ModelConfigRules.ProviderModelRules {
			if r["providerId"] == magpieID {
				ruled = append(ruled, r["modelId"].(string))
			}
		}
		slices.Sort(config)
		slices.Sort(personal)
		return config, personal, ruled
	}
	both := []string{"deepseek/flash", "deepseek/pro"}
	if c, p, _ := listed(); !slices.Equal(c, both) || !slices.Equal(p, both) {
		t.Fatalf("connected: config.json %v, provider_config.json %v", c, p)
	}
	// taken out on the Agents page: gone from both, at the next sync
	if err := provider.SetHiddenModels("zcode", []string{"deepseek/flash"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if c, p, r := listed(); !slices.Equal(c, []string{"deepseek/pro"}) || !slices.Equal(p, []string{"deepseek/pro"}) || len(r) != 0 {
		t.Fatalf("hidden: config.json %v, provider_config.json %v, rules %v", c, p, r)
	}
	// and back
	if err := provider.SetHiddenModels("zcode", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if c, p, r := listed(); !slices.Equal(c, both) || !slices.Equal(p, both) || !slices.Equal(r, []string{"deepseek/flash"}) {
		t.Fatalf("shown again: config.json %v, provider_config.json %v, rules %v", c, p, r)
	}
}
