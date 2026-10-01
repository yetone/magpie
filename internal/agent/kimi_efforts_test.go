package agent

import (
	"os"
	"path/filepath"
	"reflect"
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
