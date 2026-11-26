package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// kimiSubagentHome is a sandbox HOME with a Kimi Code config holding the
// user's own model, and magpie's provider "think" with three models: deep
// (high, max), wide (low, medium) and plain (no thinking).
func kimiSubagentHome(t *testing.T, config string) (home, path string) {
	t.Helper()
	home = t.TempDir()
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
		{ID: "deep", Efforts: []string{"high", "max"}},
		{ID: "wide", Efforts: []string{"low", "medium"}},
		{ID: "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(home, ".kimi-code", "config.toml")
	writeFile(t, path, config)
	return home, path
}

// kimiOwnConfig is Kimi Code after its own sign-in, with a model of the
// user's own that thinks at low to high.
const kimiOwnConfig = `default_model = "kimi-code/kimi-for-coding"

[models."kimi-code/kimi-for-coding"]
provider = "managed:kimi-code"
model = "kimi-for-coding"
max_context_size = 262144
capabilities = ["thinking", "tool_use"]
support_efforts = ["low", "medium", "high"]

[providers."managed:kimi-code"]
type = "kimi"
base_url = "https://api.kimi.com/coding/v1"
api_key = ""
`

// kimiSubagentsValid fails the test where Kimi Code 2.6.2 would refuse the
// config at a session's start (session/subagent/configSection.ts,
// assertValidSubagentModelPool and assertValidSubagentDefaultEffort), and
// returns its [secondary_model].
func kimiSubagentsValid(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Secondary map[string]any `toml:"secondary_model"`
		Models    map[string]struct {
			Capabilities   []string `toml:"capabilities"`
			SupportEfforts []string `toml:"support_efforts"`
		} `toml:"models"`
	}
	if err := toml.Unmarshal(b, &c); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	s := c.Secondary
	if s == nil {
		return nil
	}
	def, _ := s["default_model"].(string)
	if def == "" {
		def, _ = s["model"].(string)
	}
	var pool []string
	if m, ok := s["models"].(map[string]any); ok {
		if def == "" {
			t.Fatalf("a pool with no default_model:\n%s", b)
		}
		if _, ok := m[def]; !ok {
			t.Fatalf("default_model %q not in the pool:\n%s", def, b)
		}
		for k := range m {
			pool = append(pool, k)
		}
	} else if def != "" {
		pool = []string{def}
	}
	for _, k := range pool {
		if _, ok := c.Models[k]; !ok {
			t.Fatalf("pool entry %q names no model:\n%s", k, b)
		}
	}
	eff, _ := s["default_effort"].(string)
	if eff == "" || pool == nil {
		return s
	}
	for _, k := range pool {
		m := c.Models[k]
		always := slices.Contains(m.Capabilities, "always_thinking")
		thinks := always || slices.Contains(m.Capabilities, "thinking")
		switch {
		case eff == "off" && always:
			t.Fatalf("off on %q, which always thinks:\n%s", k, b)
		case eff == "off":
		case !thinks:
			t.Fatalf("effort %q on %q, which doesn't think:\n%s", eff, k, b)
		case len(m.SupportEfforts) > 0 && eff != "on" && !slices.Contains(m.SupportEfforts, eff):
			t.Fatalf("effort %q not among %q's %v:\n%s", eff, k, m.SupportEfforts, b)
		}
	}
	return s
}

func kimiOptionValues(f *Field, vals map[string]string) []string {
	var out []string
	for _, o := range f.Options(vals) {
		out = append(out, o.Value)
	}
	return out
}

// XiaoLing_Arisa on X: Kimi Code's subagent pool and its effort, picked
// from magpie's models, written as Kimi reads them; each write is one Kimi
// takes, and Disconnect puts back the subagent model the user had.
func TestKimiSubagentPool(t *testing.T) {
	home, path := kimiSubagentHome(t, kimiOwnConfig+`
[secondary_model]
default_model = "kimi-code/kimi-for-coding"
`)
	a := kimi(home)
	sub, pool, eff := a.Field("subagent_model"), a.Field("subagent_pool"), a.Field("subagent_effort")
	if sub == nil || pool == nil || eff == nil {
		t.Fatal("no subagent fields")
	}
	if sub.Get() != "kimi-code/kimi-for-coding" || pool.Get() != "" {
		t.Fatalf("read: %q %q", sub.Get(), pool.Get())
	}
	// not connected: magpie's models aren't offered
	if slices.ContainsFunc(kimiOptionValues(sub, a.Values()), func(v string) bool { return strings.HasPrefix(v, "magpie/") }) {
		t.Fatal("magpie's models offered before it is connected")
	}
	// one set all the same (switched on again, its subagents put back)
	// brings magpie's provider and models in beside the lead, so Kimi
	// resolves it; never a name it lacks a table for
	if err := sub.Set("magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	if s := kimiSubagentsValid(t, path); s["default_model"] != "magpie/think/deep" || a.Values()["model"] != "kimi-code/kimi-for-coding" {
		t.Fatalf("subagents on magpie's: %v, lead %q", s, a.Values()["model"])
	}
	if err := sub.Set("kimi-code/kimi-for-coding"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(kimiOptionValues(pool, a.Values()), "magpie/think/wide") {
		t.Fatalf("pool options: %v", kimiOptionValues(pool, a.Values()))
	}

	// a pool of magpie's: the user's default isn't in it, so the first is
	if err := pool.Set("magpie/think/deep\nmagpie/think/wide"); err != nil {
		t.Fatal(err)
	}
	s := kimiSubagentsValid(t, path)
	if s["default_model"] != "magpie/think/deep" || pool.Get() != "magpie/think/deep\nmagpie/think/wide" {
		t.Fatalf("pool: %v %q", s, pool.Get())
	}
	// deep and wide share no level: off only
	if got := kimiOptionValues(eff, a.Values()); !slices.Equal(got, []string{"off"}) {
		t.Fatalf("effort options for deep+wide: %v", got)
	}
	if err := eff.Set("high"); err == nil {
		t.Fatal("high taken for wide, which hasn't it")
	}

	// the user's description of an entry stays through a change
	b, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(b), `"magpie/think/deep" = ""`, `"magpie/think/deep" = "hard problems"`, 1)), 0o644)
	if err := pool.Set("magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	s = kimiSubagentsValid(t, path)
	if m, _ := s["models"].(map[string]any); m["magpie/think/deep"] != "hard problems" || len(m) != 1 {
		t.Fatalf("description: %v", s)
	}
	if got := kimiOptionValues(eff, a.Values()); !slices.Equal(got, []string{"off", "high", "max"}) {
		t.Fatalf("effort options for deep: %v", got)
	}
	if err := eff.Set("MAX"); err != nil {
		t.Fatal(err)
	}
	if s = kimiSubagentsValid(t, path); s["default_effort"] != "max" {
		t.Fatalf("effort: %v", s)
	}
	// wide joins: max is none of its, so the effort goes rather than
	// leaving a config Kimi refuses
	if err := pool.Set("magpie/think/deep\nmagpie/think/wide"); err != nil {
		t.Fatal(err)
	}
	if s = kimiSubagentsValid(t, path); s["default_effort"] != nil {
		t.Fatalf("max kept for wide: %v", s)
	}

	// the default picked as Kimi's own /secondary-model does: into the pool
	if err := sub.Set("magpie/think/plain"); err != nil {
		t.Fatal(err)
	}
	s = kimiSubagentsValid(t, path)
	if m, _ := s["models"].(map[string]any); s["default_model"] != "magpie/think/plain" || len(m) != 3 {
		t.Fatalf("default: %v", s)
	}

	// Disconnect: magpie's models out, the user's subagent model back
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	s = kimiSubagentsValid(t, path)
	b, _ = os.ReadFile(path)
	if s["default_model"] != "kimi-code/kimi-for-coding" || s["models"] != nil || strings.Contains(string(b), "magpie") {
		t.Fatalf("disconnect:\n%s", b)
	}
}

// A model leaving magpie's catalog leaves the pool before its table goes,
// so Kimi never meets a pool naming a model it lacks.
func TestKimiSubagentPoolSync(t *testing.T) {
	home, path := kimiSubagentHome(t, kimiOwnConfig)
	a := kimi(home)
	if err := a.Apply("model", "magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("subagent_pool").Set("magpie/think/wide\nkimi-code/kimi-for-coding"); err != nil {
		t.Fatal(err)
	}
	if s := kimiSubagentsValid(t, path); s["default_model"] != "magpie/think/wide" {
		t.Fatalf("pool: %v", s)
	}
	if err := provider.Save(provider.Provider{
		ID: "think", Name: "Think", Chat: "https://example.test/v1", Key: "key",
		Models: []string{"deep", "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	s := kimiSubagentsValid(t, path)
	m, _ := s["models"].(map[string]any)
	if s["default_model"] != "kimi-code/kimi-for-coding" || len(m) != 1 {
		b, _ := os.ReadFile(path)
		t.Fatalf("after sync:\n%s", b)
	}
	// Default: subagents on the lead's model, the section gone
	if err := a.Field("subagent_model").Set(""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "secondary_model") {
		t.Fatalf("default:\n%s", b)
	}
}

// The user's own pool, of their own models, is theirs: connecting and
// disconnecting leave it as written, comments and all.
func TestKimiSubagentPoolKeepsTheUsers(t *testing.T) {
	own := kimiOwnConfig + `
[secondary_model]
# subagents on the plan's model
default_model = "kimi-code/kimi-for-coding"
default_effort = "medium"

[secondary_model.models]
"kimi-code/kimi-for-coding" = "everything"
`
	home, path := kimiSubagentHome(t, own)
	a := kimi(home)
	if err := a.Apply("model", "magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != own {
		t.Fatalf("the user's pool changed:\n%s", b)
	}
}
