package edit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

const tomlPathFile = `# my harness
model = "gpt-5" # main
max_tokens = 1024

[model_providers.openai]
# my own key
name = "OpenAI"
base_url = "https://api.openai.com/v1"

[tools]
web = true
`

func tomlPathWrite(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func tomlPathRead(t *testing.T, p string) (map[string]any, string) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := toml.Unmarshal(b, &m); err != nil {
		t.Fatalf("not TOML: %v\n%s", err, b)
	}
	return m, string(b)
}

// SetTOML writes key paths as SetJSON does, as TOML: a value beside the
// user's in its table, an object as a table of its own, numbers as
// numbers, JSON text in its own order; every line it doesn't own stays as
// it was.
func TestSetTOMLPaths(t *testing.T) {
	p := tomlPathWrite(t, tomlPathFile)
	err := SetTOML(p,
		KV{Path: "model", Value: "magpie/deepseek/pro"},
		KV{Path: "model_provider", Value: "magpie"},
		KV{Path: "max_tokens", Value: int64(4096)},
		KV{Path: "model_providers.magpie", Value: json.RawMessage(`{"name":"magpie","base_url":"http://127.0.0.1:3425/v1","wire_api":"responses","headers":{"x-a":"1"}}`)},
		KV{Path: "tools.shell", Value: false},
		KV{Path: "agents.list", Value: json.RawMessage(`["a","b"]`)},
	)
	if err != nil {
		t.Fatal(err)
	}
	m, raw := tomlPathRead(t, p)
	mp := m["model_providers"].(map[string]any)
	mg := mp["magpie"].(map[string]any)
	if m["model"] != "magpie/deepseek/pro" || m["model_provider"] != "magpie" || m["max_tokens"] != int64(4096) ||
		mg["base_url"] != "http://127.0.0.1:3425/v1" || mg["headers"].(map[string]any)["x-a"] != "1" ||
		mp["openai"].(map[string]any)["name"] != "OpenAI" || m["tools"].(map[string]any)["shell"] != false ||
		m["tools"].(map[string]any)["web"] != true || len(m["agents"].(map[string]any)["list"].([]any)) != 2 {
		t.Fatalf("set:\n%s", raw)
	}
	for _, s := range []string{"# my harness", `model = "magpie/deepseek/pro" # main`, "# my own key", "[model_providers.magpie]\nname = \"magpie\"\nbase_url"} {
		if !strings.Contains(raw, s) {
			t.Fatalf("%q lost:\n%s", s, raw)
		}
	}
	if strings.Index(raw, "[model_providers.openai]") > strings.Index(raw, "[tools]") {
		t.Fatalf("order moved:\n%s", raw)
	}
	if v, ok := GetTOML(p, "model_providers.magpie.wire_api"); !ok || v != "responses" {
		t.Fatalf("get: %q %v", v, ok)
	}
}

// What GetTOMLText read goes back as it was written, a table's comments
// with it, and DelTOML takes out a table it left empty.
func TestTOMLTextPutBack(t *testing.T) {
	const mine = "model = \"gpt-5\"\n\n[model_providers.magpie]\n# the one I set up\nbase_url = \"http://mine\" # keep\n\n[x]\ny = 1\n"
	p := tomlPathWrite(t, mine)
	had, ok := GetTOMLText(p, "model_providers.magpie")
	if !ok || !had.Table || !strings.Contains(had.Text, "# the one I set up") {
		t.Fatalf("text: %+v %v", had, ok)
	}
	hadModel, _ := GetTOMLText(p, "model")
	if err := SetTOML(p, KV{Path: "model_providers.magpie", Value: map[string]any{"base_url": "http://gw"}}, KV{Path: "model", Value: "m"}, KV{Path: "z.k", Value: "v"}); err != nil {
		t.Fatal(err)
	}
	if _, raw := tomlPathRead(t, p); strings.Contains(raw, "http://mine") || !strings.Contains(raw, "[z]") {
		t.Fatalf("wired:\n%s", raw)
	}
	if err := DelTOML(p, "z.k"); err != nil {
		t.Fatal(err)
	}
	if err := SetTOML(p, KV{Path: "model_providers.magpie", Value: had}, KV{Path: "model", Value: hadModel}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != mine {
		t.Fatalf("put back:\n%s\nwant:\n%s", b, mine)
	}
}

// A file TOML's parser refuses is never written over, and reads as
// holding nothing.
func TestSetTOMLRefusesBrokenFile(t *testing.T) {
	const broken = "model = \"gpt-5\"\n[oops\nkey = 1\n"
	p := tomlPathWrite(t, broken)
	if err := SetTOML(p, KV{Path: "model", Value: "m"}); err == nil {
		t.Fatal("wrote a file that doesn't parse")
	}
	if err := DelTOML(p, "model"); err == nil {
		t.Fatal("deleted from a file that doesn't parse")
	}
	if b, _ := os.ReadFile(p); string(b) != broken {
		t.Fatalf("written over:\n%s", b)
	}
	if _, ok := GetTOMLText(p, "model"); ok {
		t.Fatal("a broken file read as having the key")
	}
	// an edit that would define a key twice is refused too
	p = tomlPathWrite(t, "[a]\nb = { c = 1 }\n")
	if err := SetTOML(p, KV{Path: "a.b.c", Value: 2}); err == nil {
		t.Fatal("wrote [a.b] beside a's inline b")
	}
}

// A quoted key is found however the file quotes it, and a new one is
// written quoted.
func TestSetTOMLQuotedKeys(t *testing.T) {
	p := tomlPathWrite(t, "[providers.\"my.vendor\"]\nkey = \"a\"\n")
	if err := SetTOML(p, KV{Path: `providers.my\.vendor.key`, Value: "b"}, KV{Path: "providers.new one.key", Value: "c"}); err != nil {
		t.Fatal(err)
	}
	m, raw := tomlPathRead(t, p)
	ps := m["providers"].(map[string]any)
	if ps["my.vendor"].(map[string]any)["key"] != "b" || ps["new one"].(map[string]any)["key"] != "c" || strings.Count(raw, "my.vendor") != 1 {
		t.Fatalf("quoted:\n%s", raw)
	}
	if err := DelTOML(p, `providers.my\.vendor.key`, "providers.new one.key"); err != nil {
		t.Fatal(err)
	}
	if _, raw := tomlPathRead(t, p); strings.TrimSpace(raw) != "" {
		t.Fatalf("left:\n%s", raw)
	}
}
