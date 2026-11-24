package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/yetone/magpie/internal/gateway"
)

// harnessSrc is a harness whose config is TOML, as Codex's is: a provider
// table of magpie's beside the user's, chosen by a top-level key
// (kaising00 on X: a plugin couldn't write TOML).
// Each string is a key of its own, so Check sees one moved by hand.
const harnessSrc = `
export const agent = { id: "harness", name: "Harness", config: "~/.harness/config.toml", model: "model" };
export function connect({ gateway, model }) {
  return {
    model_provider: "magpie",
    "model_providers.magpie.name": "magpie",
    "model_providers.magpie.base_url": gateway.v1,
    "model_providers.magpie.bearer_token": gateway.key,
    "model_providers.magpie.wire_api": "responses",
    max_tokens: model.id.endsWith("pro") ? 8192 : 4096,
  };
}
`

// harnessTableSrc writes magpie's provider as one object: a table of its
// own, [model_providers.magpie].
const harnessTableSrc = `
export const agent = { id: "harness", name: "Harness", config: "~/.harness/config.toml", model: "model" };
export function connect({ gateway }) {
  return {
    model_provider: "magpie",
    "model_providers.magpie": { name: "magpie", base_url: gateway.v1, bearer_token: gateway.key, http_headers: { "x-from": "magpie" } },
  };
}
`

const harnessMine = `# my harness
model = "gpt-5" # main
max_tokens = 1024

[model_providers.openai]
# my own key
name = "OpenAI"
base_url = "https://api.openai.com/v1"

[tools]
web = true
`

func TestPluggedTOML(t *testing.T) {
	home := pluggedHome(t, harnessSrc)
	path := filepath.Join(home, ".harness", "config.toml")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(harnessMine), 0o644)
	read := func() (map[string]any, string) {
		t.Helper()
		b, _ := os.ReadFile(path)
		var c map[string]any
		if err := toml.Unmarshal(b, &c); err != nil {
			t.Fatalf("not TOML: %v\n%s", err, b)
		}
		return c, string(b)
	}
	table := func(c map[string]any, keys ...string) map[string]any {
		for _, k := range keys {
			c, _ = c[k].(map[string]any)
		}
		return c
	}

	a, err := Find("harness")
	if err != nil {
		t.Fatal(err)
	}
	if a.Plugin == "" || !a.Detected() {
		t.Fatalf("agent: %+v", a)
	}
	f := a.Field("model")
	if f.Get() != "gpt-5" {
		t.Fatalf("get %q", f.Get())
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	c, raw := read()
	mg := table(c, "model_providers", "magpie")
	if c["model"] != "deepseek/pro" || c["model_provider"] != "magpie" || c["max_tokens"] != int64(8192) ||
		mg["base_url"] != gateway.URL()+"/v1" || mg["bearer_token"] != gateway.TokenFor("harness") ||
		table(c, "model_providers", "openai")["name"] != "OpenAI" || table(c, "tools")["web"] != true {
		t.Fatalf("wired:\n%s", raw)
	}
	for _, s := range []string{"# my harness", `model = "deepseek/pro" # main`, "# my own key"} {
		if !strings.Contains(raw, s) {
			t.Fatalf("%q lost:\n%s", s, raw)
		}
	}
	if f.Get() != "magpie/deepseek/pro" || a.Check() != "" {
		t.Fatalf("get %q, check %q", f.Get(), a.Check())
	}

	// the user edits the file while it is on magpie: a key of their own, a
	// comment, a table
	b, _ := os.ReadFile(path)
	edited := strings.Replace(string(b), "[tools]\n", "[tools]\n# shell too\nshell = true\n", 1) + "\n[history]\npersistence = \"none\"\n"
	os.WriteFile(path, []byte(edited), 0o644)
	kept := func(when string) {
		t.Helper()
		c, raw := read()
		if table(c, "tools")["shell"] != true || table(c, "history")["persistence"] != "none" || !strings.Contains(raw, "# shell too") {
			t.Fatalf("%s lost the user's edit:\n%s", when, raw)
		}
	}
	// another of magpie's models: a write again, what was stashed first kept
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if c, raw := read(); c["model"] != "deepseek/flash" || c["max_tokens"] != int64(4096) {
		t.Fatalf("flash:\n%s", raw)
	}
	kept("a reconnect")
	// the base moved by hand: Check says so
	b, _ = os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(b), gateway.URL(), "http://elsewhere", 1)), 0o644)
	if a.Check() == "" {
		t.Fatal("Check missed the base moved")
	}
	b, _ = os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(b), "http://elsewhere", gateway.URL(), 1)), 0o644)

	// Disconnect: every key back as the user had it, typed; magpie's
	// table and the keys it added gone; the user's edit kept
	if err := a.Unwire(); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	if c["model"] != "gpt-5" || c["max_tokens"] != int64(1024) || c["model_provider"] != nil ||
		table(c, "model_providers", "magpie") != nil || table(c, "model_providers", "openai")["name"] != "OpenAI" ||
		!strings.Contains(raw, `model = "gpt-5" # main`) {
		t.Fatalf("unwired:\n%s", raw)
	}
	kept("Disconnect")
	if _, ok := stashLoad()["plugin:harness:"+path]; ok {
		t.Fatal("the stash outlived the wiring")
	}
	// and connected again
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if c, raw := read(); c["model"] != "deepseek/pro" || table(c, "model_providers", "magpie")["wire_api"] != "responses" {
		t.Fatalf("again:\n%s", raw)
	}
	kept("connecting again")
	if err := f.Set("gpt-5"); err != nil {
		t.Fatal(err)
	}
	kept("its own model")
}

// A table of the user's own under a key magpie writes is put back whole,
// its comments with it, when magpie steps out.
func TestPluggedTOMLTablePutBack(t *testing.T) {
	home := pluggedHome(t, harnessTableSrc)
	path := filepath.Join(home, ".harness", "config.toml")
	const mine = "model = \"gpt-5\"\n\n[model_providers.magpie]\n# an old one of mine\nbase_url = \"http://127.0.0.1:9999/v1\" # keep\n"
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(mine), 0o644)
	a, err := Find("harness")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var c map[string]any
	if err := toml.Unmarshal(b, &c); err != nil {
		t.Fatalf("not TOML: %v\n%s", err, b)
	}
	mg, _ := c["model_providers"].(map[string]any)["magpie"].(map[string]any)
	if strings.Contains(string(b), "9999") || mg["base_url"] != gateway.URL()+"/v1" || mg["http_headers"].(map[string]any)["x-from"] != "magpie" {
		t.Fatalf("wired:\n%s", b)
	}
	if err := a.Unwire(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != mine {
		t.Fatalf("put back:\n%s\nwant:\n%s", b, mine)
	}
}

// A config that doesn't parse is unknown: magpie writes nothing over it
// and stashes nothing.
func TestPluggedTOMLBrokenFile(t *testing.T) {
	home := pluggedHome(t, harnessSrc)
	path := filepath.Join(home, ".harness", "config.toml")
	const broken = "model = \"gpt-5\"\n[model_providers.openai\nname = \"OpenAI\"\n"
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(broken), 0o644)
	a, err := Find("harness")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("magpie/deepseek/pro"); err == nil {
		t.Fatal("wrote a config that doesn't parse")
	}
	if b, _ := os.ReadFile(path); string(b) != broken {
		t.Fatalf("written over:\n%s", b)
	}
	if _, ok := stashLoad()["plugin:harness:"+path]; ok {
		t.Fatal("stashed what a broken file held")
	}
}

// own is given a TOML config parsed, a date in it too.
func TestPluggedTOMLOwnReadsFile(t *testing.T) {
	home := pluggedHome(t, `
export const agent = { id: "harness", name: "Harness", config: "~/.harness/config.toml", model: "model" };
export function connect({ gateway }) { return { "model_providers.magpie.base_url": gateway.v1 }; }
export function own({ config }) {
  return Object.keys(config.model_providers ?? {}).filter((k) => k !== "magpie")
    .map((k) => ({ value: k + "/" + config.model_providers[k].model, model: config.model_providers[k].model, direct: k + " " + config.since }));
}
`)
	path := filepath.Join(home, ".harness", "config.toml")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("since = 2026-11-24\nmodel = \"openai/gpt-5\"\n\n[model_providers.openai]\nmodel = \"gpt-5\"\n"), 0o644)
	a, err := Find("harness")
	if err != nil {
		t.Fatal(err)
	}
	f := a.Field("model")
	opts := f.Options(map[string]string{"model": f.Get()})
	if !containsOption(opts, "openai/gpt-5") {
		t.Fatalf("own models: %+v", opts)
	}
}
