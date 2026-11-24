package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agentplug"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
	"gopkg.in/yaml.v3"
)

// pluggedHome is a home of its own with DeepSeek's two models in magpie
// and the plugins whose agent modules are srcs, each in a folder with a
// package.json naming it.
func pluggedHome(t *testing.T, srcs ...string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	var l plugin.List
	for i, src := range srcs {
		dir := filepath.Join(home, "plugins", "p"+string(rune('a'+i)))
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"p`+string(rune('a'+i))+`","magpie":{"agent":"./agent.js"}}`), 0o644)
		os.WriteFile(filepath.Join(dir, "agent.js"), []byte(src), 0o644)
		l.Plugins = append(l.Plugins, plugin.Entry{Spec: dir})
	}
	os.MkdirAll(settings.Dir(), 0o755)
	b, _ := json.Marshal(l)
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	agentplug.Reload()
	t.Cleanup(agentplug.Reload)
	return home
}

const aiderSrc = `
export const agent = { id: "aider", name: "Aider", bin: "aider", config: "~/.aider.conf.yml", model: "model", prefix: "openai/", ua: ["aider"] };
export function connect({ gateway, model }) {
  return { "openai-api-base": gateway.v1, "openai-api-key": gateway.key, "map-tokens": model.id.endsWith("pro") ? 2048 : 4096 };
}
`

func TestPluggedYAML(t *testing.T) {
	home := pluggedHome(t, aiderSrc)
	path := filepath.Join(home, ".aider.conf.yml")
	mine := "# mine\nmodel: gpt-5 # main\nopenai-api-key: sk-mine\nmap-tokens: 1024\ndark-mode: true\n"
	os.WriteFile(path, []byte(mine), 0o644)
	read := func() (map[string]any, string) {
		var c map[string]any
		b, _ := os.ReadFile(path)
		yaml.Unmarshal(b, &c)
		return c, string(b)
	}

	a, err := Find("aider")
	if err != nil {
		t.Fatal(err)
	}
	if a.Plugin == "" || a.Name != "Aider" || !a.Detected() {
		t.Fatalf("agent: %+v", a)
	}
	f := a.Field("model")
	if f.Get() != "gpt-5" || a.Check() != "" {
		t.Fatalf("get %q, check %q", f.Get(), a.Check())
	}
	if !containsOption(f.Options(map[string]string{"model": f.Get()}), "magpie/deepseek/pro") {
		t.Fatal("magpie's models aren't offered")
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	c, raw := read()
	if c["model"] != "openai/deepseek/pro" || c["openai-api-base"] != gateway.URL()+"/v1" || c["openai-api-key"] != gateway.TokenFor("aider") ||
		c["map-tokens"] != 2048 || c["dark-mode"] != true || !strings.Contains(raw, "# mine") {
		t.Fatalf("wired:\n%s", raw)
	}
	if f.Get() != "magpie/deepseek/pro" || a.Check() != "" {
		t.Fatalf("get %q, check %q", f.Get(), a.Check())
	}
	// another of magpie's models keeps what was stashed first
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if c, raw := read(); c["model"] != "openai/deepseek/flash" || c["map-tokens"] != 4096 {
		t.Fatalf("flash:\n%s", raw)
	}
	// the base moved by hand: Check says so
	b, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(b), gateway.URL(), "http://elsewhere", 1)), 0o644)
	if a.Check() == "" {
		t.Fatal("Check missed the base moved")
	}
	// its own model: every key comes back as the user had it, typed
	if err := f.Set("gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	if c["model"] != "gpt-5.5" || c["openai-api-key"] != "sk-mine" || c["map-tokens"] != 1024 || c["dark-mode"] != true ||
		c["openai-api-base"] != nil || !strings.Contains(raw, "# mine") {
		t.Fatalf("own:\n%s", raw)
	}
	if _, ok := stashLoad()["plugin:aider:"+path]; ok {
		t.Fatal("the stash outlived the wiring")
	}
	// the gateway names its requests by the key, and by the UA it says
	if usage.AgentOf("aider/0.86.2") != "aider" {
		t.Fatalf("UA: %q", usage.AgentOf("aider/0.86.2"))
	}
}

func TestPluggedJSONUnwire(t *testing.T) {
	home := pluggedHome(t, `
export default {
  agent: { id: "jot", config: "$HOME/.jot/settings.json", model: "llm.model" },
  async connect({ gateway, model, models }) {
    return { "llm.endpoint": { url: gateway.v1, key: gateway.key }, "llm.window": model.context, "llm.count": models.length };
  },
};`)
	path := filepath.Join(home, ".jot", "settings.json")
	a, err := Find("jot")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "jot" || a.Detected() {
		t.Fatal("found with no file")
	}
	f := a.Field("model")
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	var c struct {
		LLM struct {
			Model    string
			Endpoint struct{ URL, Key string }
			Count    int
		}
	}
	b, _ := os.ReadFile(path)
	json.Unmarshal(b, &c)
	if c.LLM.Model != "deepseek/pro" || c.LLM.Endpoint.URL != gateway.URL()+"/v1" || c.LLM.Count < 2 || f.Get() != "magpie/deepseek/pro" {
		t.Fatalf("wired: %s", b)
	}
	// Disconnect takes every key out, as there was nothing before
	if err := a.Unwire(); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	var left map[string]any
	json.Unmarshal(b, &left)
	if llm, _ := left["llm"].(map[string]any); len(llm) != 0 {
		t.Fatalf("unwired: %s", b)
	}
}

func TestPluggedRefused(t *testing.T) {
	pluggedHome(t,
		`export const agent = { id: "codex", config: "~/x.json", model: "m" };`,
		`export const agent = { id: "bad", config: "~/x.ini", model: "m" };`,
		`export const agent = { id: "boom", config: "~/.boom.json", model: "m" };
export function connect() { throw new Error("no gateway for me"); }`,
		`syntax error here (`,
	)
	errs := PluginErrors()
	var got []string
	for _, e := range errs {
		got = append(got, e)
	}
	all := strings.Join(got, "\n")
	if !strings.Contains(all, `"codex" is magpie's own`) || !strings.Contains(all, "agent.format") || len(errs) != 3 {
		t.Fatalf("errors: %v", errs)
	}
	codex, _ := Find("codex")
	if codex == nil || codex.Plugin != "" {
		t.Fatal("a plugin took codex's place")
	}
	// connect throws: the error says so and the file isn't written
	a, err := Find("boom")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("magpie/deepseek/pro"); err == nil || !strings.Contains(err.Error(), "no gateway for me") {
		t.Fatalf("set: %v", err)
	}
	if _, err := os.Stat(a.Path); err == nil {
		t.Fatal("written though connect threw")
	}
}

func containsOption(opts []Option, v string) bool {
	for _, o := range opts {
		if o.Value == v {
			return true
		}
	}
	return false
}
