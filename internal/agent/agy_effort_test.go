package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// agy has no effort magpie can write (xyenon on Discord): settings.json has
// no key for it and --effort, for one session, is turned away for magpie's
// models. Its row picks one, kept in magpie (provider.AgentEffort "agy"),
// among the levels its models have, offered only while agy is on magpie's;
// settings.json is never touched for it.
func TestAgyEffort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "think", Name: "Think", Chat: "https://think.test/v1", Key: "key", Models: []string{"gemini-3.8-flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("think", "https://think.test/v1", []catalog.Model{{ID: "gemini-3.8-flash", Efforts: []string{"low", "high"}}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"model":"mine","modelProvider":"vertex"}`), 0o644)
	a, err := Find("agy")
	if err != nil {
		t.Fatal(err)
	}
	f := a.Field("effort")
	if f == nil {
		t.Fatal("no effort field")
	}
	values := func() []string {
		var out []string
		for _, o := range f.Options(map[string]string{}) {
			out = append(out, o.Value)
		}
		return out
	}
	if got := values(); got != nil {
		t.Fatalf("offered %q while on the user's own model", got)
	}
	if err := a.Apply("model", "magpie/think/gemini-3.8-flash"); err != nil {
		t.Fatal(err)
	}
	if got := values(); !slices.Equal(got, []string{"", "low", "high"}) {
		t.Fatalf("offered %q", got)
	}
	before, _ := os.ReadFile(path)
	if err := f.Set("high"); err != nil {
		t.Fatal(err)
	}
	if got, kept := f.Get(), provider.AgentEffort("agy"); got != "high" || kept != "high" {
		t.Fatalf("picked high: shown %q, kept %q", got, kept)
	}
	if provider.AgentEffort("antigravity") != "" {
		t.Fatal("agy's pick went to Antigravity's app")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("the pick touched settings.json:\n%s\nwas\n%s", after, before)
	}
	if err := f.Set("hard"); err == nil {
		t.Fatal("a word that isn't a level was kept")
	}
	if err := f.Set(""); err != nil || provider.AgentEffort("agy") != "" {
		t.Fatalf("back to the default: %v, kept %q", err, provider.AgentEffort("agy"))
	}
}
