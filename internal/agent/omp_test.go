package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
	"gopkg.in/yaml.v3"
)

func TestOmp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
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

	// another role through magpie keeps it in models.yml
	f.Set("magpie/deepseek/flash")
	os.WriteFile(configPath, []byte("modelRoles:\n  default: magpie/deepseek/flash\n  smol: magpie/deepseek/pro\n"), 0o644)
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	c, _ = read(configPath)
	m, _ = read(modelsPath)
	if roles(c)["default"] != nil || roles(c)["smol"] != "magpie/deepseek/pro" || providers(m)["magpie"] == nil {
		t.Fatalf("reset with smol: %v %v", c, m)
	}

	// reset: only default goes, and magpie with it
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
