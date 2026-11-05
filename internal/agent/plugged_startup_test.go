package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agentplug"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// pigSrc is an agent plugin as agent-pig is: PiG (a Go port of Pi) keeps
// its providers in models.json and the model a session starts on in
// settings.json, as defaultProvider and defaultModel (PiG's
// docs/site/docs/settings.md), so connect writes models.json and startup
// names the model in settings.json.
const pigSrc = `
export const agent = {
  id: "pig", name: "PiG", bin: "pig", dir: "~/.pig/agent",
  config: "~/.pig/agent/models.json", format: "json",
  startup: { config: "~/.pig/agent/settings.json", model: "defaultModel", keys: { defaultProvider: "magpie" } },
};
export function connect({ gateway, models }) {
  return { "providers.magpie": { name: "Magpie", baseUrl: gateway.v1, apiKey: gateway.key, api: "openai-completions", models: models.map((m) => ({ id: m.id })) } };
}
`

// pigSettingsMine and pigModelsMine are PiG's two files as its user wrote
// them, on a provider of their own.
const pigSettingsMine = `{
  "defaultProvider": "anthropic",
  "defaultModel": "claude-sonnet-4",
  "theme": "dark"
}
`

const pigModelsMine = `{
  "providers": {
    "ollama": { "baseUrl": "http://localhost:11434/v1", "api": "openai-completions", "models": [{ "id": "qwen3" }] }
  }
}
`

func pigHome(t *testing.T) (a *Agent, settingsPath, modelsPath string) {
	t.Helper()
	home := pluggedHome(t, pigSrc)
	dir := filepath.Join(home, ".pig", "agent")
	os.MkdirAll(dir, 0o755)
	settingsPath, modelsPath = filepath.Join(dir, "settings.json"), filepath.Join(dir, "models.json")
	os.WriteFile(settingsPath, []byte(pigSettingsMine), 0o644)
	os.WriteFile(modelsPath, []byte(pigModelsMine), 0o644)
	a, err := Find("pig")
	if err != nil {
		t.Fatal(err)
	}
	setUpInMagpie(a.ID)
	return a, settingsPath, modelsPath
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	var m map[string]any
	if err != nil || json.Unmarshal(b, &m) != nil {
		t.Fatalf("%s: %v\n%s", path, err, b)
	}
	return m
}

// An agent plugin whose agent keeps its startup model apart from its
// provider list (str_ on Discord: PiG's row only listed models, where Pi's
// picks the one it starts on) gets a model field: picking one of magpie's
// writes connect's provider into models.json and defaultProvider and
// defaultModel into settings.json, a model picked in PiG among magpie's
// is still magpie's, the catalog follows, and switching it off puts both
// files back as the user had them.
func TestPluggedStartupModel(t *testing.T) {
	a, settingsPath, modelsPath := pigHome(t)
	f := a.Field("model")
	if f == nil || a.Field("provider") != nil || a.ListsModels {
		t.Fatalf("fields %+v, lists models %v", a.Fields, a.ListsModels)
	}
	if !containsOption(f.Options(nil), "magpie/deepseek/pro") {
		t.Fatalf("magpie's models aren't offered: %+v", f.Options(nil))
	}
	if f.Get() != "claude-sonnet-4" || a.Wired() {
		t.Fatalf("get %q, wired %v before connecting", f.Get(), a.Wired())
	}

	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	s := readJSONFile(t, settingsPath)
	if s["defaultProvider"] != "magpie" || s["defaultModel"] != "deepseek/pro" || s["theme"] != "dark" {
		t.Fatalf("settings.json: %v", s)
	}
	ps := readJSONFile(t, modelsPath)["providers"].(map[string]any)
	m, _ := ps["magpie"].(map[string]any)
	if m == nil || m["baseUrl"] != gateway.URL()+"/v1" || m["apiKey"] != gateway.TokenFor("pig") || ps["ollama"] == nil {
		t.Fatalf("models.json: %v", ps)
	}
	if _, ok := readJSONFile(t, modelsPath)["defaultModel"]; ok {
		t.Fatal("the model went into models.json")
	}
	if f.Get() != "magpie/deepseek/pro" || !a.Wired() || a.Check() != "" || a.Drift() != nil {
		t.Fatalf("get %q, wired %v, check %q, drift %+v", f.Get(), a.Wired(), a.Check(), a.Drift())
	}

	// picked in PiG's /model and saved: another of magpie's
	b, _ := os.ReadFile(settingsPath)
	os.WriteFile(settingsPath, []byte(strings.Replace(string(b), `"deepseek/pro"`, `"deepseek/flash"`, 1)), 0o644)
	if f.Get() != "magpie/deepseek/flash" || !a.Wired() || a.Drift() != nil {
		t.Fatalf("PiG's own pick: get %q, wired %v, drift %+v", f.Get(), a.Wired(), a.Drift())
	}

	// the catalog changed: the list follows, the model PiG starts on stays
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "max"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, x := range readJSONFile(t, modelsPath)["providers"].(map[string]any)["magpie"].(map[string]any)["models"].([]any) {
		ids = append(ids, x.(map[string]any)["id"].(string))
	}
	if !slices.Contains(ids, "deepseek/max") || readJSONFile(t, settingsPath)["defaultModel"] != "deepseek/flash" {
		t.Fatalf("after sync: %v, %v", ids, readJSONFile(t, settingsPath))
	}

	// off: both files as the user had them
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	fileAsMine(t, settingsPath, pigSettingsMine)
	if ps := readJSONFile(t, modelsPath)["providers"].(map[string]any); ps["magpie"] != nil || ps["ollama"] == nil {
		t.Fatalf("models.json after off: %v", ps)
	}
	if a.Wired() || f.Get() != "claude-sonnet-4" {
		t.Fatalf("after off: wired %v, get %q", a.Wired(), f.Get())
	}
}

// A model of their own picked in PiG while magpie's provider is in its
// list is the user's: still connected, as joined, and switching it off
// takes magpie's provider out and leaves the user's pick where it is.
func TestPluggedStartupOwnPickKept(t *testing.T) {
	a, settingsPath, modelsPath := pigHome(t)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	mine := "{\n  \"defaultProvider\": \"ollama\",\n  \"defaultModel\": \"qwen3\",\n  \"theme\": \"dark\"\n}\n"
	os.WriteFile(settingsPath, []byte(mine), 0o644)
	if a.Field("model").Get() != "qwen3" || !a.Wired() || a.Drift() != nil {
		t.Fatalf("get %q, wired %v, drift %+v", a.Field("model").Get(), a.Wired(), a.Drift())
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	fileAsMine(t, settingsPath, mine)
	if ps := readJSONFile(t, modelsPath)["providers"].(map[string]any); ps["magpie"] != nil {
		t.Fatalf("magpie's provider left: %v", ps)
	}
	if _, ok := stashLoad()["plugin:pig:"+settingsPath]; ok {
		t.Fatal("the startup stash outlived the wiring")
	}
}

// The plugin gone from disk, `magpie pig default` puts both files back
// from the stash alone.
func TestPluggedStartupRestoreGone(t *testing.T) {
	a, settingsPath, modelsPath := pigHome(t)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(a.Plugin)
	os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), []byte(`{"plugins":[]}`), 0o644)
	agentplug.Reload()
	g, ok, err := RestoreGone("pig")
	if err != nil || !ok || len(g.Put) != 2 {
		t.Fatalf("restored %+v, %v, %v", g, ok, err)
	}
	fileAsMine(t, settingsPath, pigSettingsMine)
	if ps := readJSONFile(t, modelsPath)["providers"].(map[string]any); ps["magpie"] != nil || ps["ollama"] == nil {
		t.Fatalf("models.json: %v", ps)
	}
}

// startup is checked as config is, and says one model: with agent.model
// too, or in config's own file, it is refused.
func TestPluggedStartupRefused(t *testing.T) {
	pluggedHome(t,
		`export const agent = { id: "a1", config: "~/.a1/m.json", model: "model", startup: { config: "~/.a1/s.json", model: "m" } };
export function connect() { return { k: 1 }; }`,
		`export const agent = { id: "a2", config: "~/.a2/m.json", startup: { config: "~/.a2/m.json", model: "m" } };
export function connect() { return { k: 1 }; }`,
		`export const agent = { id: "a3", config: "~/.a3/m.json", startup: { config: "~/.a3/s.toml", model: "m" } };
export function connect() { return { k: 1 }; }`,
		`export const agent = { id: "a4", config: "~/.a4/m.json", startup: { config: "~/.a4/s.json", model: "m", keys: { m: "x" } } };
export function connect() { return { k: 1 }; }`,
		`export const agent = { id: "a5", config: "~/.a5/m.json", startup: { config: "~/.a5/s.json", model: "m" } };`,
	)
	errs := PluginErrors()
	if len(errs) != 5 {
		t.Fatalf("errors: %v", errs)
	}
	for _, e := range errs {
		if !strings.Contains(e, "agent.startup") && !strings.Contains(e, "agent.model") {
			t.Fatalf("error: %q", e)
		}
	}
}
