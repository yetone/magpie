package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

func TestJoyCode(t *testing.T) {
	home, _ := museHome(t)
	dir := filepath.Join(home, ".joycode")
	os.MkdirAll(dir, 0o755)
	providers := filepath.Join(dir, "model-providers.json")
	cfg := filepath.Join(dir, "config.toml")
	// a user's own DongColor-style provider and model, as JoyCode keeps them
	origProviders := `{
  "version": 1,
  "providers": {
    "dongcolor-chat": {
      "name": "DongColor Chat",
      "base_url": "http://llm-gw.jd.local",
      "models_url": "http://llm-gw.jd.local/v1/models",
      "enabled_models": [],
      "api_key": "sk-user"
    }
  },
  "models": []
}
`
	os.WriteFile(providers, []byte(origProviders), 0o600)
	os.WriteFile(cfg, []byte("model = \"GPT-5.6 Sol\"\npersonality = \"pragmatic\"\n"), 0o600)

	readProviders := func() map[string]any {
		t.Helper()
		var m map[string]any
		b, _ := os.ReadFile(providers)
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		return m
	}

	a := joycode(home)
	if a.Path != cfg || a.Dir != dir || !a.Detected() {
		t.Fatalf("agent: path=%s dir=%s detected=%v", a.Path, a.Dir, a.Detected())
	}
	f := a.Field("model")
	if f.Get() != "GPT-5.6 Sol" || a.Check() != "" {
		t.Fatalf("own: %q %q", f.Get(), a.Check())
	}
	var vals []string
	for _, o := range f.Options(a.Values()) {
		vals = append(vals, o.Value)
	}
	if !strings.Contains(strings.Join(vals, " "), "GPT-5.6 Sol magpie/deepseek/pro") {
		t.Fatalf("options: %v", vals)
	}

	for range 2 { // twice: one magpie provider, user's DongColor kept
		if err := f.Set("magpie/deepseek/pro"); err != nil {
			t.Fatal(err)
		}
	}
	m := readProviders()
	ps, _ := m["providers"].(map[string]any)
	if len(ps) != 2 {
		t.Fatalf("providers: %v", m["providers"])
	}
	dc, _ := ps["dongcolor-chat"].(map[string]any)
	if dc["api_key"] != "sk-user" || dc["base_url"] != "http://llm-gw.jd.local" {
		t.Fatalf("dongcolor kept: %v", dc)
	}
	mine, _ := ps["magpie"].(map[string]any)
	if mine["name"] != "magpie" || mine["base_url"] != gateway.URL() || mine["models_url"] != gatewayV1()+"/models" ||
		mine["api_key"] != gateway.TokenFor("joycode") {
		t.Fatalf("magpie provider: %#v", mine)
	}
	ids, _ := mine["enabled_models"].([]any)
	if !reflect.DeepEqual(ids, []any{"deepseek/pro", "deepseek/flash"}) && !reflect.DeepEqual(ids, []any{"deepseek/flash", "deepseek/pro"}) {
		t.Fatalf("enabled_models: %#v", ids)
	}
	if v, _ := edit.GetTOMLTop(cfg, "model"); v != "magpie/deepseek/pro" {
		t.Fatalf("model: %q", v)
	}
	if v, _ := edit.GetTOMLTop(cfg, "personality"); v != "pragmatic" {
		t.Fatalf("toml kept: personality=%q", v)
	}
	if f.Get() != "magpie/deepseek/pro" || a.Check() != "" || !a.Wired() {
		t.Fatalf("on: %q check=%q wired=%v", f.Get(), a.Check(), a.Wired())
	}

	// base_url pointed elsewhere
	b, _ := os.ReadFile(providers)
	os.WriteFile(providers, []byte(strings.Replace(string(b), gateway.URL(), "http://elsewhere", 1)), 0o600)
	if !strings.Contains(a.Check(), "http://elsewhere") {
		t.Fatalf("check: %q", a.Check())
	}
	os.WriteFile(providers, b, 0o600)

	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if mine, _ := readProviders()["providers"].(map[string]any)["magpie"].(map[string]any); mine["base_url"] != gateway.URL() || mine["api_key"] != gateway.TokenFor("joycode") {
		t.Fatalf("sync: %#v", mine)
	}

	// stepping out puts the user's model back and drops magpie
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetTOMLTop(cfg, "model"); v != "GPT-5.6 Sol" {
		t.Fatalf("restore model: %q", v)
	}
	m = readProviders()
	ps, _ = m["providers"].(map[string]any)
	if _, ok := ps["magpie"]; ok || len(ps) != 1 {
		t.Fatalf("drop magpie: %v", m["providers"])
	}
	if a.Wired() || a.Check() != "" {
		t.Fatalf("off: wired=%v check=%q", a.Wired(), a.Check())
	}
}

func TestJoyCodeNewFiles(t *testing.T) {
	home, _ := museHome(t)
	a := joycode(home)
	f := a.Field("model")
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	providers := filepath.Join(home, ".joycode", "model-providers.json")
	cfg := filepath.Join(home, ".joycode", "config.toml")
	var m map[string]any
	b, _ := os.ReadFile(providers)
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["version"] != float64(1) {
		t.Fatalf("version: %v", m["version"])
	}
	ps, _ := m["providers"].(map[string]any)
	mine, _ := ps["magpie"].(map[string]any)
	if mine["base_url"] != gateway.URL() || mine["models_url"] != gatewayV1()+"/models" {
		t.Fatalf("provider: %#v", mine)
	}
	if v, _ := edit.GetTOMLTop(cfg, "model"); v != "magpie/deepseek/flash" {
		t.Fatalf("model: %q", v)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := edit.GetJSON(providers, "providers.magpie"); ok {
		t.Fatal("magpie provider left")
	}
}
