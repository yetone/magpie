package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// #333: the new Kimi Code is told each thinking model's levels
// (support_efforts, default_effort high where there is one), or its picker
// has thinking on and off only; none is its own off, so it isn't a level,
// and kimi-cli, which knows neither key, gets neither.
func TestKimiEfforts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("KIMI_SHARE_DIR", "")
	t.Setenv("KIMI_CODE_HOME", "")
	if err := provider.Save(provider.Provider{
		ID: "think", Name: "Think", Chat: "https://example.test/v1", Key: "key",
		Models: []string{"deep", "wide", "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("think", "https://example.test/v1", []catalog.Model{
		{ID: "deep", Efforts: []string{"none", "high", "max"}},
		{ID: "wide", Efforts: []string{"low", "medium"}},
		{ID: "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	type model struct {
		Capabilities   []string `toml:"capabilities"`
		SupportEfforts []string `toml:"support_efforts"`
		DefaultEffort  string   `toml:"default_effort"`
	}
	read := func(path string) map[string]model {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var c struct{ Models map[string]model }
		if err := toml.Unmarshal(b, &c); err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		return c.Models
	}

	path := filepath.Join(home, ".kimi-code", "config.toml")
	writeFile(t, path, "")
	if err := kimi(home).Field("model").Set("magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	ms := read(path)
	for k, want := range map[string]model{
		"magpie/think/deep":  {[]string{"thinking", "tool_use"}, []string{"high", "max"}, "high"},
		"magpie/think/wide":  {[]string{"thinking", "tool_use"}, []string{"low", "medium"}, ""},
		"magpie/think/plain": {[]string{"tool_use"}, nil, ""},
	} {
		if got := ms[k]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v, want %+v", k, got, want)
		}
	}

	os.RemoveAll(filepath.Join(home, ".kimi-code"))
	legacy := filepath.Join(home, ".kimi", "config.toml")
	writeFile(t, legacy, "")
	if err := kimi(home).Field("model").Set("magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	if got := read(legacy)["magpie/think/deep"]; got.SupportEfforts != nil || got.DefaultEffort != "" {
		t.Errorf("kimi-cli's: %+v", got)
	}
}

// @apple_test_src on X: Kimi Code had no thinking level to pick where
// every other agent had one. It is Kimi Code's [thinking] (enabled,
// effort), as its own /model saves it, the rest of the table kept; the
// levels are the current model's; kimi-cli has default_thinking only.
func TestKimiThinkingField(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("KIMI_SHARE_DIR", "")
	t.Setenv("KIMI_CODE_HOME", "")
	path := filepath.Join(home, ".kimi-code", "config.toml")
	writeFile(t, path, `default_model = "deep"

[models.deep]
provider = "p"
model = "deep"
capabilities = ["thinking"]
support_efforts = ["low", "high", "max"]
default_effort = "high"

[models.plain]
provider = "p"
model = "plain"

[models.bare]
provider = "p"
model = "bare"
capabilities = ["thinking"]

[models.always]
provider = "p"
model = "always"
support_efforts = ["low"]

[models.always.overrides]
capabilities = ["thinking", "always_thinking"]
support_efforts = ["medium", "max"]

[thinking]
keep = "all"
`)
	f := kimi(home).Field("effort")
	if f == nil {
		t.Fatal("Kimi Code has no thinking field")
	}
	type th struct {
		Enabled *bool  `toml:"enabled"`
		Effort  string `toml:"effort"`
		Keep    string `toml:"keep"`
	}
	read := func() *th {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var c struct{ Thinking *th }
		if err := toml.Unmarshal(b, &c); err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		return c.Thinking
	}
	yes, no := true, false
	for _, step := range []struct {
		set  string
		want th
	}{
		{"max", th{&yes, "max", "all"}},
		{"off", th{&no, "max", "all"}},
		{"low", th{&yes, "low", "all"}},
		{"", th{nil, "", "all"}},
	} {
		if err := f.Set(step.set); err != nil {
			t.Fatal(err)
		}
		if got := read(); got == nil || !reflect.DeepEqual(*got, step.want) {
			t.Fatalf("set %q: [thinking] %+v, want %+v", step.set, got, step.want)
		}
		if got := f.Get(); got != step.set {
			t.Errorf("set %q, read back %q", step.set, got)
		}
	}
	// with nothing of the user's beside it, Default takes the table out
	writeFile(t, path, "[models.deep]\nprovider = \"p\"\n\n[thinking]\nenabled = false\n")
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != nil {
		t.Errorf("Default left [thinking] %+v", *got)
	}
	if got := f.Get(); got != "" {
		t.Errorf("no [thinking] reads %q", got)
	}

	writeFile(t, path, `[models.deep]
capabilities = ["thinking"]
support_efforts = ["low", "high", "max"]
default_effort = "high"

[models.plain]
model = "plain"

[models.bare]
capabilities = ["thinking"]

[models.always]
support_efforts = ["low"]

[models.always.overrides]
capabilities = ["thinking", "always_thinking"]
support_efforts = ["medium", "max"]
`)
	vals := func(opts []Option) []string {
		out := []string{}
		for _, o := range opts {
			out = append(out, o.Value+"/"+o.Takes)
		}
		return out
	}
	for model, want := range map[string][]string{
		"deep":    {"/high", "off/", "low/", "high/", "max/"},
		"plain":   {},
		"bare":    {"off/"},
		"always":  {"medium/", "max/"},
		"unknown": {"off/", "low/", "medium/", "high/"},
	} {
		if got := vals(f.Options(map[string]string{"model": model})); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: levels %v, want %v", model, got, want)
		}
	}

	// kimi-cli: default_thinking, on or off
	os.RemoveAll(filepath.Join(home, ".kimi-code"))
	legacy := filepath.Join(home, ".kimi", "config.toml")
	writeFile(t, legacy, "default_model = \"x\"\n")
	f = kimi(home).Field("effort")
	if got := vals(f.Options(nil)); !reflect.DeepEqual(got, []string{"off/", "on/"}) {
		t.Errorf("kimi-cli levels %v", got)
	}
	for _, step := range []struct{ set, line string }{
		{"on", "default_thinking = true"},
		{"off", "default_thinking = false"},
		{"", ""},
	} {
		if err := f.Set(step.set); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(legacy)
		if step.line != "" && !strings.Contains(string(b), step.line) || step.line == "" && strings.Contains(string(b), "default_thinking") {
			t.Errorf("set %q: %s", step.set, b)
		}
		if got := f.Get(); got != step.set {
			t.Errorf("kimi-cli: set %q, read back %q", step.set, got)
		}
	}
}
