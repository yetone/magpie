package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

func TestKimi(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("KIMI_SHARE_DIR", "")
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".kimi", "config.toml")
	os.MkdirAll(filepath.Dir(path), 0o755)
	// as kimi writes it after a Kimi Code sign-in
	os.WriteFile(path, []byte(`default_model = "kimi-code/kimi-for-coding"
default_thinking = true

[models."kimi-code/kimi-for-coding"]
provider = "managed:kimi-code"
model = "kimi-for-coding"
max_context_size = 262144

[providers."managed:kimi-code"]
type = "kimi"
base_url = "https://api.kimi.com/coding/v1"
api_key = ""

[providers."managed:kimi-code".oauth]
storage = "file"
key = "oauth/kimi-code"

[loop_control]
max_steps_per_turn = 100
`), 0o644)
	read := func() string { b, _ := os.ReadFile(path); return string(b) }
	table := func(name string) map[string]string {
		t.Helper()
		got, err := edit.GetTOMLTable(path, name)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// what kimi's own check wants: the default among the models, and each
	// model's provider among the providers
	valid := func() {
		t.Helper()
		var c struct {
			Default   string `toml:"default_model"`
			Models    map[string]struct{ Provider string }
			Providers map[string]any
		}
		if err := toml.Unmarshal([]byte(read()), &c); err != nil {
			t.Fatalf("%v\n%s", err, read())
		}
		if _, ok := c.Models[c.Default]; c.Default != "" && !ok {
			t.Fatalf("default %q is not a model:\n%s", c.Default, read())
		}
		for k, m := range c.Models {
			if c.Providers[m.Provider] == nil {
				t.Fatalf("%s: provider %q missing:\n%s", k, m.Provider, read())
			}
		}
	}
	a := kimi(home)
	if a.Path != path {
		t.Fatalf("path: %s", a.Path)
	}
	f := a.Field("model")
	if f.Get() != "kimi-code/kimi-for-coding" {
		t.Fatalf("get: %q", f.Get())
	}
	var own []string
	for _, o := range f.Options(a.Values()) {
		if !strings.HasPrefix(o.Value, "magpie/") {
			own = append(own, o.Value)
		}
	}
	if strings.Join(own, ",") != "kimi-code/kimi-for-coding" {
		t.Fatalf("own options: %v", own)
	}

	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	valid()
	raw := read()
	p := table("providers.magpie")
	m := table(`models."magpie/deepseek/pro"`)
	if p["type"] != "kimi" || p["api_key"] != "magpie" || !strings.HasSuffix(p["base_url"], "/v1") ||
		m["provider"] != "magpie" || m["model"] != "deepseek/pro" || m["max_context_size"] == "" ||
		table(`models."magpie/deepseek/flash"`) == nil {
		t.Fatalf("tables:\n%s", raw)
	}
	if f.Get() != "magpie/deepseek/pro" || table(`providers."managed:kimi-code".oauth`)["key"] != "oauth/kimi-code" ||
		table("loop_control")["max_steps_per_turn"] != "100" || !strings.Contains(raw, "default_thinking = true") {
		t.Fatalf("config:\n%s", raw)
	}
	if a.Check() != "" {
		t.Fatalf("check: %s", a.Check())
	}

	// a provider added later reaches Kimi's picker
	if err := provider.Save(provider.Provider{ID: "glm", Name: "GLM", Chat: "https://open.bigmodel.cn/api/paas/v4", Key: "k", Models: []string{"glm-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	valid()
	if table(`models."magpie/glm/glm-5"`) == nil || strings.Count(read(), "[providers.magpie]") != 1 {
		t.Fatalf("sync:\n%s", read())
	}

	// its wiring gone while the default is still magpie's
	edit.SetTOMLKey(path, "providers.magpie", "base_url", "http://elsewhere/v1")
	if a.Check() == "" {
		t.Fatal("check missed the moved base_url")
	}

	// reset: back to the Kimi Code model the user had, magpie's tables gone
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	valid()
	raw = read()
	if f.Get() != "kimi-code/kimi-for-coding" || strings.Contains(raw, "magpie") {
		t.Fatalf("reset:\n%s", raw)
	}
	// and Sync, with magpie out, writes nothing back
	if err := a.Sync(); err != nil || read() != raw {
		t.Fatalf("sync after reset: %v\n%s", err, read())
	}

	// its own model from magpie: magpie's tables go too
	f.Set("magpie/deepseek/flash")
	if err := f.Set("kimi-code/kimi-for-coding"); err != nil {
		t.Fatal(err)
	}
	valid()
	if strings.Contains(read(), "magpie") {
		t.Fatalf("own:\n%s", read())
	}
}

// A kimi that has never run has no config; magpie writes one Kimi takes.
func TestKimiNoConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("KIMI_SHARE_DIR", filepath.Join(home, "share"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	a := kimi(home)
	if a.Path != filepath.Join(home, "share", "config.toml") {
		t.Fatalf("path: %s", a.Path)
	}
	f := a.Field("model")
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(a.Path)
	var c map[string]any
	if err := toml.Unmarshal(b, &c); err != nil || c["default_model"] != "magpie/deepseek/pro" {
		t.Fatalf("%v\n%s", err, b)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(a.Path)
	if strings.Contains(string(b), "magpie") {
		t.Fatalf("reset:\n%s", b)
	}
}
