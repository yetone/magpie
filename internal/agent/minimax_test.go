package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"gopkg.in/yaml.v3"
)

// the config.yaml MiniMax Code writes after a MiniMax sign-in and a
// provider added by hand (mcode provider add), trimmed
const miniMaxConfig = `# my notes
logLevel: info
defaultModel: minimax/MiniMax-M2.7 # the usual
defaultModelVariant: thinking
memory:
  enabled: false
custom_provider:
  work:
    name: work
    kind: custom
    enabled: true
    api: openai-completions
    options:
      apiKey: sk-mine
      baseURL: https://llm.example.test/v1
      authMode: api-key
    models:
      deep-reasoner-1:
        limit:
          context: 200000
provider:
  minimax:
    name: MiniMax
    models:
      MiniMax-M2.7:
        name: MiniMax-M2.7
`

func miniMaxHome(t *testing.T) (home, path string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("MINIMAX_DATA_DIR", "")
	if err := provider.Save(provider.Provider{ID: "think", Name: "Think", Chat: "https://example.test/v1", Key: "k",
		Models: []string{"deep", "v1.5-flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("think", "https://example.test/v1", []catalog.Model{
		{ID: "deep", Efforts: []string{"none", "low", "high"}, Context: 400000, Output: 64000, Images: true},
		{ID: "v1.5-flash"},
	}); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(home, ".minimax", "config.yaml")
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(miniMaxConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, path
}

type miniMaxModel struct {
	Name  string
	Limit struct {
		Context int
		Output  int
	}
	Reasoning bool
	Thinking  struct {
		EffortOptions []string `yaml:"effortOptions"`
		DefaultEffort string   `yaml:"defaultEffort"`
	}
	Capabilities map[string]any
	Enabled      *bool
}

type miniMaxFile struct {
	DefaultModel        string `yaml:"defaultModel"`
	DefaultModelVariant string `yaml:"defaultModelVariant"`
	Memory              map[string]any
	CustomProvider      map[string]struct {
		Name    string
		Kind    string
		Enabled *bool
		API     string
		Options map[string]any
		Models  map[string]miniMaxModel
	} `yaml:"custom_provider"`
	Provider map[string]any
}

func readMiniMax(t *testing.T, path string) (miniMaxFile, string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c miniMaxFile
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	return c, string(b)
}

// #362: a model through magpie is custom_provider.magpie's, an Anthropic
// messages provider at the gateway with the catalog as its models, and
// defaultModel names it as MiniMax Code splits it (at the first slash); the
// user's providers, keys and comments stay, and stepping out puts back the
// default and variant they had.
func TestMiniMaxCode(t *testing.T) {
	home, path := miniMaxHome(t)
	visionOff(t) // flash is text-only only while no model describes images to it
	a := miniMax(home)
	if a.Path != path || !a.Detected() {
		t.Fatalf("path %q, detected %v", a.Path, a.Detected())
	}
	f := a.Field("model")
	if f.Get() != "minimax/MiniMax-M2.7" {
		t.Fatalf("get: %q", f.Get())
	}
	if err := f.Set("magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	c, raw := readMiniMax(t, path)
	mp, ok := c.CustomProvider["magpie"]
	if !ok || c.DefaultModel != "custom_provider:magpie/think~deep" || c.DefaultModelVariant != "" {
		t.Fatalf("config:\n%s", raw)
	}
	if mp.Kind != "custom" || mp.API != "anthropic-messages" || mp.Enabled == nil || !*mp.Enabled ||
		mp.Options["baseURL"] != gateway.URL() || mp.Options["apiKey"] != gateway.Token || mp.Options["authMode"] != "api-key" ||
		!reflect.DeepEqual(mp.Options["headers"], map[string]any{"User-Agent": "minimax-code"}) {
		t.Fatalf("magpie's entry:\n%s", raw)
	}
	deep, flash := mp.Models["think~deep"], mp.Models["think~v1.5-flash"]
	if len(mp.Models) != 2 || deep.Limit.Context != 400000 || deep.Limit.Output != 64000 || !deep.Reasoning ||
		!reflect.DeepEqual(deep.Thinking.EffortOptions, []string{"low", "high"}) || deep.Thinking.DefaultEffort != "high" ||
		deep.Capabilities["support_image"] != true {
		t.Fatalf("deep:\n%s", raw)
	}
	if flash.Reasoning || flash.Thinking.EffortOptions != nil || flash.Capabilities != nil || flash.Limit.Context != 0 {
		t.Fatalf("flash:\n%s", raw)
	}
	if c.CustomProvider["work"].Options["apiKey"] != "sk-mine" || c.Provider["minimax"] == nil || c.Memory["enabled"] != false ||
		!strings.Contains(raw, "# my notes") || !strings.Contains(raw, "# the usual") {
		t.Fatalf("the user's own:\n%s", raw)
	}
	if f.Get() != "magpie/think/deep" || a.Check() != "" {
		t.Fatalf("get %q, check %q", f.Get(), a.Check())
	}

	// the picker: the user's models as MiniMax Code names them, then magpie's
	var vals []string
	for _, o := range f.Options(map[string]string{"model": f.Get()}) {
		vals = append(vals, o.Value)
	}
	for _, want := range []string{"minimax/MiniMax-M2.7", "custom_provider:work/deep-reasoner-1", "magpie/think/deep", "magpie/think/v1.5-flash"} {
		if !strings.Contains(" "+strings.Join(vals, " ")+" ", " "+want+" ") {
			t.Fatalf("options %v lack %s", vals, want)
		}
	}
	for _, v := range vals {
		if strings.HasPrefix(v, "custom_provider:magpie/") {
			t.Fatalf("magpie's entry offered as the user's own: %v", vals)
		}
	}

	// another of magpie's keeps the default stashed first; MiniMax Code's
	// own keys on magpie's entry and its models stay through a rewrite
	os.WriteFile(path, []byte(strings.Replace(raw, "      think~deep:\n", "      think~deep:\n        enabled: false # hidden in /model\n", 1)), 0o644)
	if err := f.Set("magpie/think/v1.5-flash"); err != nil {
		t.Fatal(err)
	}
	c, raw = readMiniMax(t, path)
	if d := c.CustomProvider["magpie"].Models["think~deep"]; c.DefaultModel != "custom_provider:magpie/think~v1.5-flash" ||
		d.Enabled == nil || *d.Enabled || !strings.Contains(raw, "# hidden in /model") {
		t.Fatalf("second:\n%s", raw)
	}

	// broken wiring shows
	edit := strings.Replace(raw, "apiKey: "+gateway.Token, "apiKey: other", 1)
	os.WriteFile(path, []byte(edit), 0o644)
	if a.Check() == "" {
		t.Fatalf("check missed a changed key:\n%s", edit)
	}

	// back to the default: what the user had, variant too, magpie's entry gone
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	c, raw = readMiniMax(t, path)
	if c.DefaultModel != "minimax/MiniMax-M2.7" || c.DefaultModelVariant != "thinking" || c.CustomProvider["magpie"].Kind != "" ||
		c.CustomProvider["work"].Kind != "custom" || !strings.Contains(raw, "# my notes") {
		t.Fatalf("reset:\n%s", raw)
	}

	// one of the user's own from magpie: magpie's entry goes, no variant
	f.Set("magpie/think/deep")
	if err := f.Set("custom_provider:work/deep-reasoner-1"); err != nil {
		t.Fatal(err)
	}
	c, raw = readMiniMax(t, path)
	if c.DefaultModel != "custom_provider:work/deep-reasoner-1" || c.DefaultModelVariant != "" || c.CustomProvider["magpie"].Kind != "" {
		t.Fatalf("own:\n%s", raw)
	}
}

// Sync rewrites magpie's models as the catalog is now — only where magpie's
// entry is — and leaves the file alone when nothing changed.
func TestMiniMaxCodeSync(t *testing.T) {
	home, path := miniMaxHome(t)
	a := miniMax(home)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != miniMaxConfig {
		t.Fatalf("sync without magpie's entry wrote:\n%s", b)
	}
	if err := a.Field("model").Set("magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "think", Name: "Think", Chat: "https://example.test/v1", Key: "k",
		Models: []string{"deep", "v1.5-flash", "v2"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	c, raw := readMiniMax(t, path)
	if _, ok := c.CustomProvider["magpie"].Models["think~v2"]; !ok || len(c.CustomProvider["magpie"].Models) != 3 {
		t.Fatalf("sync:\n%s", raw)
	}
	st, _ := os.Stat(path)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if st2, _ := os.Stat(path); !st2.ModTime().Equal(st.ModTime()) {
		b, _ := os.ReadFile(path)
		if string(b) != raw {
			t.Fatalf("an unchanged catalog rewrote the file:\n%s", b)
		}
	}
}

// Stepping out of magpie leaves the file as it was: no empty custom_provider
// where magpie's entry was the only one.
func TestMiniMaxCodeLeavesNoEmptyProviders(t *testing.T) {
	home, path := miniMaxHome(t)
	const own = "# mine\ndefaultModel: minimax/MiniMax-M2.7\n"
	os.WriteFile(path, []byte(own), 0o644)
	f := miniMax(home).Field("model")
	if err := f.Set("magpie/think/deep"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != own {
		t.Fatalf("left:\n%s", b)
	}
}

// MINIMAX_DATA_DIR moves MiniMax Code's whole data folder, config.yaml too.
func TestMiniMaxCodeDataDir(t *testing.T) {
	d := filepath.Join(t.TempDir(), "mm")
	t.Setenv("MINIMAX_DATA_DIR", d)
	if a := miniMax(t.TempDir()); a.Path != filepath.Join(d, "config.yaml") || a.Dir != d {
		t.Fatalf("dir %q path %q", a.Dir, a.Path)
	}
}

// #1387 (worksyang): MiniMax Code 3.1.0 takes a SubAgent's model only
// when it matches mcodeOneSlash, and magpie's is
// "custom_provider:magpie/<model>", so the model under magpie's entry has
// no slash: bb-codex/gpt-6.1-sol is written as bb-codex~gpt-6.1-sol, which
// the gateway takes back to that provider's model. A config an older
// magpie wrote moves over on the next sync — the user's keys on a model
// and every ref to it — its slashed key kept, switched off, so a session
// begun on it still resumes; stepping out still puts back what they had.
func TestMiniMaxCodeOneSlash(t *testing.T) {
	home, path := miniMaxHome(t)
	if err := provider.Save(provider.Provider{ID: "bb-codex", Name: "BB Codex", Chat: "https://example.test/v1", Key: "k",
		Models: []string{"gpt-6.1-sol"}}); err != nil {
		t.Fatal(err)
	}
	a := miniMax(home)
	f := a.Field("model")
	if err := f.Set("magpie/bb-codex/gpt-6.1-sol"); err != nil {
		t.Fatal(err)
	}
	c, raw := readMiniMax(t, path)
	if c.DefaultModel != "custom_provider:magpie/bb-codex~gpt-6.1-sol" || f.Get() != "magpie/bb-codex/gpt-6.1-sol" {
		t.Fatalf("get %q:\n%s", f.Get(), raw)
	}
	oneSlash(t, raw)

	// as an older magpie wrote it, the user's keys on it and their
	// SubAgent memory models pointed at it
	old := strings.ReplaceAll(raw, "~", "/")
	old = strings.Replace(old, "      bb-codex/gpt-6.1-sol:\n", "      bb-codex/gpt-6.1-sol:\n        headers: # mine\n          X-Team: core\n", 1)
	old = strings.Replace(old, "memory:\n  enabled: false\n", "memory:\n  enabled: false\n  captureModel: custom_provider:magpie/bb-codex/gpt-6.1-sol\n  consolidationModel: minimax/MiniMax-M2.7\n", 1)
	old += "defaultLightModel: custom_provider:magpie/think/v1.5-flash\n"
	if old == raw || !strings.Contains(old, "X-Team") || !strings.Contains(old, "captureModel") {
		t.Fatalf("fixture:\n%s", old)
	}
	os.WriteFile(path, []byte(old), 0o644)
	if f.Get() != "magpie/bb-codex/gpt-6.1-sol" {
		t.Fatalf("an older magpie's default reads as %q", f.Get())
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	c, raw = readMiniMax(t, path)
	ms := c.CustomProvider["magpie"].Models
	sol, was := ms["bb-codex~gpt-6.1-sol"], ms["bb-codex/gpt-6.1-sol"]
	if c.DefaultModel != "custom_provider:magpie/bb-codex~gpt-6.1-sol" ||
		c.Memory["captureModel"] != "custom_provider:magpie/bb-codex~gpt-6.1-sol" || c.Memory["consolidationModel"] != "minimax/MiniMax-M2.7" ||
		!strings.Contains(raw, "defaultLightModel: custom_provider:magpie/think~v1.5-flash") ||
		sol.Enabled != nil || was.Enabled == nil || *was.Enabled || strings.Count(raw, "X-Team: core") != 2 || strings.Count(raw, "# mine") != 2 {
		t.Fatalf("moved over:\n%s", raw)
	}
	oneSlash(t, raw)
	if f.Get() != "magpie/bb-codex/gpt-6.1-sol" || a.Check() != "" {
		t.Fatalf("get %q check %q", f.Get(), a.Check())
	}
	st, _ := os.Stat(path)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != raw {
		t.Fatalf("a second sync changed it:\n%s", b)
	} else if st2, _ := os.Stat(path); !st2.ModTime().Equal(st.ModTime()) {
		t.Fatal("a second sync rewrote it")
	}

	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	c, raw = readMiniMax(t, path)
	if c.DefaultModel != "minimax/MiniMax-M2.7" || c.DefaultModelVariant != "thinking" || c.CustomProvider["magpie"].Kind != "" {
		t.Fatalf("reset:\n%s", raw)
	}
}

// mcodeOneSlash is MiniMax Code 3.1.0's check on a SubAgent's model, as
// worksyang quoted it in #1387 (uQn in @minimax-ai/code's chunk-2XPMNZVR.js)
var mcodeOneSlash = regexp.MustCompile(`^[^/\s]+\/[^/\s]+$`)

// oneSlash fails when a ref to magpie's entry in raw, or a model under it
// that is switched on, would not pass mcodeOneSlash
func oneSlash(t *testing.T, raw string) {
	t.Helper()
	var c miniMaxFile
	yaml.Unmarshal([]byte(raw), &c)
	refs := 0
	for _, m := range regexp.MustCompile(`custom_provider:magpie/\S+`).FindAllString(raw, -1) {
		refs++
		if !mcodeOneSlash.MatchString(m) {
			t.Fatalf("%s fails MiniMax Code's check:\n%s", m, raw)
		}
	}
	for k, m := range c.CustomProvider["magpie"].Models {
		if (m.Enabled == nil || *m.Enabled) && !mcodeOneSlash.MatchString("custom_provider:magpie/"+k) {
			t.Fatalf("model %s fails MiniMax Code's check:\n%s", k, raw)
		}
	}
	if refs == 0 {
		t.Fatalf("no ref to magpie's entry:\n%s", raw)
	}
}
