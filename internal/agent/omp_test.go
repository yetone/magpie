package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
	"gopkg.in/yaml.v3"
)

func TestOmp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, k := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(k, "")
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".omp", "agent")
	configPath, modelsPath := filepath.Join(dir, "config.yml"), filepath.Join(dir, "models.yml")
	read := func(p string) (map[string]any, string) {
		var m map[string]any
		b, _ := os.ReadFile(p)
		yaml.Unmarshal(b, &m)
		return m, string(b)
	}
	roles := func(c map[string]any) map[string]any { r, _ := c["modelRoles"].(map[string]any); return r }
	providers := func(m map[string]any) map[string]any { p, _ := m["providers"].(map[string]any); return p }
	os.MkdirAll(dir, 0o755)
	os.WriteFile(configPath, []byte("# mine\ntheme: dark\nmodelRoles:\n  default: anthropic/claude-opus-5 # main\n  smol: openai/gpt-6-mini\n"), 0o644)
	// an older omp's models.json, not yet moved to models.yml
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{"mine":{"baseUrl":"https://x","apiKey":"X","api":"openai-completions","models":[{"id":"a"}]}}}`), 0o644)

	f := omp(home).Field("model")
	if f.Get() != "anthropic/claude-opus-5" {
		t.Fatalf("get: %q", f.Get())
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	c, raw := read(configPath)
	if roles(c)["default"] != "magpie/deepseek/pro" || roles(c)["smol"] != "openai/gpt-6-mini" || c["theme"] != "dark" ||
		!strings.Contains(raw, "# mine") || !strings.Contains(raw, "# main") {
		t.Fatalf("config:\n%s", raw)
	}
	m, raw := read(modelsPath)
	ps := providers(m)
	mp, ok := ps["magpie"].(map[string]any)
	if !ok || ps["mine"] == nil || mp["auth"] != "none" || mp["api"] != "openai-completions" || !strings.HasSuffix(mp["baseUrl"].(string), "/v1") {
		t.Fatalf("models:\n%s", raw)
	}
	ids := map[string]bool{}
	for _, x := range mp["models"].([]any) {
		ids[x.(map[string]any)["id"].(string)] = true
	}
	if !ids["deepseek/pro"] || !ids["deepseek/flash"] {
		t.Fatalf("models: %v", ids)
	}

	// its own model: magpie steps out
	if err := f.Set("moonshotai/kimi-k3"); err != nil {
		t.Fatal(err)
	}
	c, _ = read(configPath)
	m, _ = read(modelsPath)
	if roles(c)["default"] != "moonshotai/kimi-k3" || providers(m)["magpie"] != nil || providers(m)["mine"] == nil {
		t.Fatalf("own: %v %v", c, m)
	}

	// another role through magpie keeps it in models.yml; the reset puts
	// back the model default had before magpie took it over
	f.Set("magpie/deepseek/flash")
	os.WriteFile(configPath, []byte("modelRoles:\n  default: magpie/deepseek/flash\n  smol: magpie/deepseek/pro\n"), 0o644)
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	c, _ = read(configPath)
	m, _ = read(modelsPath)
	if roles(c)["default"] != "moonshotai/kimi-k3" || roles(c)["smol"] != "magpie/deepseek/pro" || providers(m)["magpie"] == nil {
		t.Fatalf("reset with smol: %v %v", c, m)
	}

	// reset with nothing stashed: only default goes, and magpie with it
	os.WriteFile(configPath, []byte("theme: dark\nmodelRoles:\n  default: magpie/deepseek/flash\n  smol: openai/gpt-6-mini\n"), 0o644)
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	c, _ = read(configPath)
	m, _ = read(modelsPath)
	if roles(c)["default"] != nil || roles(c)["smol"] != "openai/gpt-6-mini" || c["theme"] != "dark" || providers(m)["magpie"] != nil || providers(m)["mine"] == nil {
		t.Fatalf("reset: %v %v", c, m)
	}
}

// omp names models beyond modelRoles.default: the other roles, the retry
// fallback chains, enabledModels and the task agents' overrides. magpie
// stays in models.yml while any of them is on it, and a provider renamed
// takes them along, their thinking levels and the comments around kept.
func TestOmpRefsBeyondDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, k := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(k, "")
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".omp", "agent")
	configPath, modelsPath := filepath.Join(dir, "config.yml"), filepath.Join(dir, "models.yml")
	os.MkdirAll(dir, 0o755)
	hasMagpie := func() bool {
		var m struct{ Providers map[string]any }
		b, _ := os.ReadFile(modelsPath)
		yaml.Unmarshal(b, &m)
		return m.Providers["magpie"] != nil
	}
	f := omp(home).Field("model")

	// the last role leaves magpie; a fallback chain still goes through it
	os.WriteFile(configPath, []byte("modelRoles:\n  default: magpie/deepseek/flash\nretry:\n  fallbackChains:\n    default:\n      - magpie/deepseek/pro:max\n"), 0o644)
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if !hasMagpie() {
		t.Fatal("a fallback chain on magpie/deepseek/pro:max lost magpie's provider")
	}
	// none left: it goes
	os.WriteFile(configPath, []byte("modelRoles:\n  default: magpie/deepseek/flash\nretry:\n  fallbackChains:\n    default:\n      - openai/gpt-6\n"), 0o644)
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if hasMagpie() {
		t.Fatal("magpie's provider stayed with nothing on it")
	}

	const before = `# mine
modelRoles:
  default: magpie/deepseek/pro
  slow: magpie/deepseek/pro:max, openai/gpt-6 # slow ones
  plan:
    - magpie/deepseek/flash:high
    - openai/gpt-6
retry:
  fallbackChains:
    # tried in order
    default:
      - magpie/deepseek/flash:max
      - magpie/deepseekx/flash
    magpie/deepseek/pro:
      - magpie/deepseek/*
enabledModels:
  - magpie/deepseek/pro
  - path: /work
    models:
      - magpie/deepseek/flash
task:
  agentModelOverrides:
    explore: magpie/deepseek/flash,openai/gpt-6
    review: [magpie/deepseek/pro:low]
`
	os.WriteFile(configPath, []byte(before), 0o644)
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	moved, err := RenameProvider("deepseek", "ds")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(configPath)
	got := string(b)
	want := strings.NewReplacer("magpie/deepseek/", "magpie/ds/").Replace(before)
	var gotDoc, wantDoc any
	yaml.Unmarshal(b, &gotDoc)
	yaml.Unmarshal([]byte(want), &wantDoc)
	gotY, _ := yaml.Marshal(gotDoc)
	wantY, _ := yaml.Marshal(wantDoc)
	if string(gotY) != string(wantY) || strings.Contains(got, "magpie/deepseek/") || !strings.Contains(got, "magpie/deepseekx/flash") ||
		!strings.Contains(got, "# mine") || !strings.Contains(got, "# slow ones") || !strings.Contains(got, "# tried in order") {
		t.Fatalf("renamed:\n%s", got)
	}
	if !slices.Contains(moved, "omp") {
		t.Fatalf("moved: %v", moved)
	}
}

// omp 16.x asks with Bun's User-Agent (Bun/1.3.14), which the gateway can't
// tell for omp's; magpie's provider names it, and the gateway knows that
// name for omp.
func TestOmpNamedToTheGateway(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, k := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(k, "")
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	if err := omp(home).Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	var m struct {
		Providers map[string]struct {
			Headers map[string]string `yaml:"headers"`
		} `yaml:"providers"`
	}
	b, _ := os.ReadFile(filepath.Join(home, ".omp", "agent", "models.yml"))
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	ua := m.Providers[magpieID].Headers["User-Agent"]
	if ua == "" || usage.AgentOf(ua) != "omp" {
		t.Fatalf("magpie's provider names omp as %q, which the gateway takes for %q:\n%s", ua, usage.AgentOf(ua), b)
	}
	if usage.AgentOf("Bun/1.3.14") == "omp" {
		t.Fatal("Bun's own User-Agent is taken for omp's, so the header proves nothing")
	}
}

// omp's own agents pick their models apart from the main one: through
// magpie on its own, a role brings magpie's provider in, has its wiring
// checked, and takes the provider out again when it is the last.
func TestOmpRoles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, k := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(k, "")
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".omp", "agent")
	configPath, modelsPath := filepath.Join(dir, "config.yml"), filepath.Join(dir, "models.yml")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(configPath, []byte("modelRoles:\n  default: anthropic/claude-opus-5\n"), 0o644)
	roles := func() map[string]any {
		var c map[string]any
		b, _ := os.ReadFile(configPath)
		yaml.Unmarshal(b, &c)
		r, _ := c["modelRoles"].(map[string]any)
		return r
	}
	magpieIn := func() bool { v, ok := edit.GetYAML(modelsPath, "providers.magpie.baseUrl"); return ok && v != "" }

	a := omp(home)
	sub, small := a.Field("subagent"), a.Field("small")
	if small.Label != "smol" || a.Field("designer") != nil {
		t.Fatalf("small labelled %q; designer: %+v", small.Label, a.Field("designer"))
	}
	for _, k := range []string{"subagent", "small", "slow"} {
		if f := a.Field(k); f == nil || !f.Quiet {
			t.Fatalf("field %s: %+v", k, f)
		}
	}
	if err := sub.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if r := roles(); r["task"] != "magpie/deepseek/flash" || r["default"] != "anthropic/claude-opus-5" || !magpieIn() {
		t.Fatalf("subagent on magpie: %v, magpie in models.yml: %v", r, magpieIn())
	}
	if sub.Get() != "magpie/deepseek/flash" {
		t.Fatalf("get: %q", sub.Get())
	}
	// the main model isn't magpie's, the subagents' is: its wiring is checked
	if a.Check() != "" {
		t.Fatalf("check, wired: %q", a.Check())
	}
	edit.SetYAML(modelsPath, edit.KV{Path: "providers.magpie.baseUrl", Value: "http://127.0.0.1:1/v1"})
	if a.Check() == "" {
		t.Fatal("check missed the subagents' broken wiring")
	}
	a.Sync()

	if err := small.Set("openai/gpt-6-mini"); err != nil {
		t.Fatal(err)
	}
	if r := roles(); r["smol"] != "openai/gpt-6-mini" || !magpieIn() {
		t.Fatalf("small, own: %v", r)
	}
	// the last role on magpie goes: so does magpie's provider
	if err := sub.Set(""); err != nil {
		t.Fatal(err)
	}
	if r := roles(); r["task"] != nil || r["smol"] != "openai/gpt-6-mini" || r["default"] != "anthropic/claude-opus-5" || magpieIn() {
		t.Fatalf("subagent reset: %v, magpie in models.yml: %v", r, magpieIn())
	}
}

// A role's thinking level ("…:max") stays with it when another model is
// picked, and the model the user had before magpie comes back, level and
// all, when the role is reset.
func TestOmpLevels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", t.TempDir())
	for _, k := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(k, "")
	}
	for _, p := range []provider.Provider{
		{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}},
		{ID: "other", Name: "Other", Chat: "https://other.example/v1", Key: "k", Models: []string{"pro"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(home, ".omp", "agent")
	configPath := filepath.Join(dir, "config.yml")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(configPath, []byte("modelRoles:\n  default: anthropic/claude-opus-5:max\n  slow: openai/gpt-6:auto\n"), 0o644)
	a := omp(home)
	get := func(k string) string { return a.Field(k).Get() }
	apply := func(k, v, want string) {
		t.Helper()
		if err := a.Apply(k, v); err != nil {
			t.Fatal(err)
		}
		if got := get(k); got != want {
			t.Fatalf("%s set to %q: %q, want %q", k, v, got, want)
		}
	}

	// deepseek/pro has no max of its own: omp clamps it, so it stays
	apply("model", "magpie/deepseek/pro", "magpie/deepseek/pro:max")
	if d := a.Drift(); d != nil {
		t.Fatalf("drift on what magpie set: %+v", d)
	}
	apply("model", "magpie/deepseek/flash", "magpie/deepseek/flash:max")
	apply("model", "magpie/deepseek/pro:low", "magpie/deepseek/pro:low") // its own level wins
	// offered at its level beside the model: that keeps the level, the
	// model alone clears it
	var at, plain bool
	for _, o := range a.Field("model").Options(a.Values()) {
		at = at || o.Value == "magpie/deepseek/pro:low" && strings.HasSuffix(o.Label, " · low")
		plain = plain || o.Value == "magpie/deepseek/pro"
	}
	if !at || !plain {
		t.Fatalf("options: at its level %v, the model %v", at, plain)
	}
	apply("model", "magpie/deepseek/pro:low", "magpie/deepseek/pro:low")
	apply("model", "magpie/deepseek/pro", "magpie/deepseek/pro")
	apply("model", "magpie/deepseek/flash", "magpie/deepseek/flash")
	apply("model", "magpie/deepseek/pro:low", "magpie/deepseek/pro:low")
	apply("slow", "magpie/deepseek/pro", "magpie/deepseek/pro:auto")

	// the first model stashed, not the magpie ones after it
	apply("model", "", "anthropic/claude-opus-5:max")
	apply("slow", "", "openai/gpt-6:auto")
	// a colon that is no level is the model's
	apply("model", "ollama/qwen3:8b", "ollama/qwen3:8b:max")

	// a model of the user's own in between is the one put back
	apply("model", "magpie/deepseek/pro", "magpie/deepseek/pro:max")
	apply("model", "openai/gpt-6", "openai/gpt-6:max")
	apply("model", "magpie/deepseek/pro", "magpie/deepseek/pro:max")
	apply("model", "", "openai/gpt-6:max")

	// a provider removed moves the role to the same model elsewhere, level
	// and all
	apply("model", "magpie/deepseek/pro", "magpie/deepseek/pro:max")
	if _, err := Reseat(func() error { return provider.Delete("deepseek") }); err != nil {
		t.Fatal(err)
	}
	if got := get("model"); got != "magpie/other/pro:max" {
		t.Fatalf("reseated: %q", got)
	}
}

// A role on one of magpie's models with a thinking level is that model
// wherever magpie matches a value against the picker: offered once, typed
// at a level on the command line, missed when omp puts it back to its own,
// and moved, level and all, when its provider is renamed.
func TestOmpLevelSuffix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", t.TempDir())
	for _, k := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(k, "")
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".omp", "agent")
	configPath := filepath.Join(dir, "config.yml")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(configPath, []byte("modelRoles:\n  default: anthropic/claude-opus-5:max\n"), 0o644)
	a := omp(home)
	f := a.Field("model")

	v, err := a.Spell("model", "magpie/deepseek/pro:high")
	if err != nil || v != "magpie/deepseek/pro:high" {
		t.Fatalf("spell at a level: %q %v", v, err)
	}
	if err := a.Apply("model", v); err != nil {
		t.Fatal(err)
	}
	// the subagents on the same model at another level: moved with it too
	if err := a.Apply("subagent", "magpie/deepseek/pro:max"); err != nil {
		t.Fatal(err)
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift on what magpie set: %+v", d)
	}
	// omp's own /model puts the role back on the user's model
	edit.SetYAML(configPath, edit.KV{Path: "modelRoles.default", Value: "anthropic/claude-opus-5:high"})
	if d := a.Drift(); d == nil || d.Kind != "replaced" || d.Want != "magpie/deepseek/pro:high" {
		t.Fatalf("drift, replaced: %+v", d)
	}
	if err := a.Reapply(); err != nil || f.Get() != "magpie/deepseek/pro:high" {
		t.Fatalf("reapplied: %q %v", f.Get(), err)
	}

	if _, err := RenameProvider("deepseek", "ds"); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "magpie/ds/pro:high" || a.Field("subagent").Get() != "magpie/ds/pro:max" {
		t.Fatalf("renamed: %q, subagent %q", f.Get(), a.Field("subagent").Get())
	}
}

// A role omp falls back through, a list of models (a YAML list or the
// comma-separated string omp also takes), is the user's to have back when
// magpie lets go of the role; a level on its last model is no role's level.
func TestOmpRoleList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, k := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(k, "")
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".omp", "agent")
	configPath := filepath.Join(dir, "config.yml")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(configPath, []byte("modelRoles:\n  slow:\n    - openai/gpt-6\n    - anthropic/claude-opus-5:high\n  task: openai/gpt-6,anthropic/claude-opus-5:high\n"), 0o644)
	a := omp(home)
	for _, k := range []string{"slow", "subagent"} {
		if got := a.Field(k).Get(); got != "openai/gpt-6,anthropic/claude-opus-5:high" {
			t.Fatalf("%s reads %q", k, got)
		}
		if err := a.Apply(k, "magpie/deepseek/pro"); err != nil {
			t.Fatal(err)
		}
		if got := a.Field(k).Get(); got != "magpie/deepseek/pro" {
			t.Fatalf("%s on magpie: %q", k, got)
		}
		if err := a.Apply(k, ""); err != nil {
			t.Fatal(err)
		}
		if got := a.Field(k).Get(); got != "openai/gpt-6,anthropic/claude-opus-5:high" {
			t.Fatalf("%s reset: %q", k, got)
		}
	}
}

// A role on magpie at a thinking level that a fallback chain names too: a
// provider renamed moves each once, the level kept. The role back on omp's
// own, the chain alone keeps magpie's provider, has its wiring checked, and
// applying again mends it.
func TestOmpRoleAndChain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, k := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(k, "")
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".omp", "agent")
	configPath, modelsPath := filepath.Join(dir, "config.yml"), filepath.Join(dir, "models.yml")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(configPath, []byte("modelRoles:\n  default: anthropic/claude-opus-5\nretry:\n  fallbackChains:\n    slow:\n      - magpie/deepseek/pro:max\n      - openai/gpt-6\n"), 0o644)
	a := omp(home)
	if err := a.Apply("slow", "magpie/deepseek/pro:max"); err != nil {
		t.Fatal(err)
	}
	if _, err := RenameProvider("deepseek", "ds"); err != nil {
		t.Fatal(err)
	}
	chain := edit.GetYAMLList(configPath, "retry.fallbackChains.slow")
	if s := a.Field("slow").Get(); s != "magpie/ds/pro:max" || !slices.Equal(chain, []string{"magpie/ds/pro:max", "openai/gpt-6"}) {
		t.Fatalf("renamed: slow %q, chain %v", s, chain)
	}

	if err := a.Apply("slow", ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetYAML(modelsPath, "providers.magpie.baseUrl"); v == "" || a.Field("slow").Get() != "" {
		t.Fatalf("the chain lost magpie's provider, or slow stayed: %q", a.Field("slow").Get())
	}
	edit.SetYAML(modelsPath, edit.KV{Path: "providers.magpie.baseUrl", Value: "http://127.0.0.1:1/v1"})
	if d := a.Drift(); d == nil || d.Kind != "unwired" {
		t.Fatalf("a chain's broken wiring: %+v", d)
	}
	if err := a.Reapply(); err != nil {
		t.Fatal(err)
	}
	if c := a.Check(); c != "" {
		t.Fatalf("applying again left: %s", c)
	}
}

// A role that is a list of models omp falls back through is the user's own,
// even when it starts on one of magpie's: magpie taking the role over
// stashes it, and a reset puts it back written as it was — a YAML list or a
// comma-separated string.
func TestOmpListRole(t *testing.T) {
	for _, slow := range []string{
		" [magpie/deepseek/flash, openai/gpt-6]",
		" magpie/deepseek/flash, openai/gpt-6",
		"\n    - magpie/deepseek/flash\n    - openai/gpt-6",
	} {
		t.Run(slow, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
			for _, k := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
				t.Setenv(k, "")
			}
			if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(home, ".omp", "agent")
			configPath := filepath.Join(dir, "config.yml")
			os.MkdirAll(dir, 0o755)
			before := "modelRoles:\n  default: anthropic/claude-opus-5\n  slow:" + slow + "\n"
			os.WriteFile(configPath, []byte(before), 0o644)
			a := omp(home)
			if err := a.Apply("slow", "magpie/deepseek/pro"); err != nil {
				t.Fatal(err)
			}
			if err := a.Apply("slow", ""); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(configPath)
			if string(b) != before {
				t.Fatalf("reset:\n%s\nwant:\n%s", b, before)
			}
		})
	}
}

// A provider renamed moves the magpie models in a list role once, the list
// written as it was, and in the list the stash keeps for a role magpie took
// over, which a reset then puts back.
func TestOmpListRoleRenamed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, k := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(k, "")
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".omp", "agent")
	configPath := filepath.Join(dir, "config.yml")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(configPath, []byte("modelRoles:\n  default: anthropic/claude-opus-5\n  slow: [magpie/deepseek/flash, openai/gpt-6]\n  task: [magpie/deepseek/pro:high, openai/gpt-6]\n"), 0o644)
	a := omp(home)
	if err := a.Apply("slow", "magpie/deepseek/pro:max"); err != nil {
		t.Fatal(err)
	}
	if _, err := RenameProvider("deepseek", "ds"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(configPath)
	task := edit.GetYAMLList(configPath, "modelRoles.task")
	if s := a.Field("slow").Get(); s != "magpie/ds/pro:max" || !slices.Equal(task, []string{"magpie/ds/pro:high", "openai/gpt-6"}) || !strings.Contains(string(b), "task: [") {
		t.Fatalf("renamed: slow %q\n%s", s, b)
	}
	if err := a.Apply("slow", ""); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(configPath)
	if !strings.Contains(string(b), "slow: [magpie/ds/flash, openai/gpt-6]") {
		t.Fatalf("reset:\n%s", b)
	}
}

// The model picker offers the models of the providers the user added to
// models.yml, beside models.dev's for the current provider, spelled as
// ownOptions spells them (the name as the note): not magpie's own entry, nor
// a provider whose models omp only discovers at run time. A model named in
// both keeps what models.dev knows of it, and a provider in both stays one
// group. Every role offers them, the subagents' as the model's.
func TestOmpOwnModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, k := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(k, "")
	}
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"zai":{"name":"Z.AI","models":{"glm-5":{"id":"glm-5","name":"GLM-5"},"glm-6":{"id":"glm-6","name":"GLM-6"}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	dir := filepath.Join(home, ".omp", "agent")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "config.yml"), []byte("modelRoles:\n  default: zai/glm-5\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "models.yml"), []byte(`providers:
  codemaker:
    baseUrl: https://codemaker.example/v1
    api: openai-completions
    models:
      - id: claude-opus-5-5
        name: Claude Opus 5.5
      - id: glm-5
  zai:
    models:
      - id: glm-5
  lan:
    baseUrl: http://127.0.0.1:8000/v1
    api: openai-completions
    discovery:
      type: openai-models-list
  zzrelay:
    models:
      - id: m1
  magpie:
    baseUrl: http://127.0.0.1:1/v1
    auth: none
    models:
      - id: deepseek/pro
`), 0o644)

	opts := omp(home).Field("model").Options(map[string]string{"model": "zai/glm-5"})
	byValue := map[string]Option{}
	for _, o := range opts {
		if _, dup := byValue[o.Value]; dup {
			t.Fatalf("%s offered twice: %+v", o.Value, opts)
		}
		byValue[o.Value] = o
	}
	if o := byValue["codemaker/claude-opus-5-5"]; o.Note != "Claude Opus 5.5" || o.Label != "" || o.Group != "codemaker" {
		t.Fatalf("codemaker model: %+v in %+v", o, opts)
	}
	if o, ok := byValue["codemaker/glm-5"]; !ok || o.Note != "" {
		t.Fatalf("model without a name: %+v in %+v", o, opts)
	}
	// models.yml's zai glm-5 has no name; models.dev's has one
	if o := byValue["zai/glm-5"]; o.Note != "GLM-5" || o.Icon == "" {
		t.Fatalf("zai/glm-5 lost models.dev's name or icon: %+v in %+v", o, opts)
	}
	// magpie has no provider here, so its entry's model comes from nowhere else
	for v := range byValue {
		if strings.HasPrefix(v, "lan/") || v == "magpie/deepseek/pro" {
			t.Fatalf("%s offered: %+v", v, opts)
		}
	}
	// zai is in models.yml and on models.dev, zzrelay after it: one group each
	seen := map[string]bool{}
	for i, o := range opts {
		if i > 0 && o.Group != opts[i-1].Group {
			if seen[o.Group] {
				t.Fatalf("%s's models split in two: %+v", o.Group, opts)
			}
		}
		seen[o.Group] = true
	}
	if _, ok := byValue["zai/glm-6"]; !ok {
		t.Fatalf("models.dev's zai/glm-6 not offered: %+v", opts)
	}
	sub := omp(home).Field("subagent").Options(map[string]string{"model": "zai/glm-5"})
	if !slices.ContainsFunc(sub, func(o Option) bool { return o.Value == "codemaker/claude-opus-5-5" }) {
		t.Fatalf("the subagents' picker lacks models.yml's models: %+v", sub)
	}
}
