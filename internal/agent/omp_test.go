package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
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

// ompModelsHome is a home whose omp is on zai/glm-5, with models.yml
// providers of the user's own, one only discovered, and magpie's entry.
func ompModelsHome(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, k := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(k, "")
	}
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"zai":{"name":"Z.AI","models":{"glm-5":{"id":"glm-5","name":"GLM-5"}}}}`), 0o644)
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
  magpie:
    baseUrl: http://127.0.0.1:1/v1
    auth: none
    models:
      - id: deepseek/pro
`), 0o644)
	return home
}

// ompOptions is the model picker's options by value, each offered once.
func ompOptions(t *testing.T, home string) map[string]Option {
	t.Helper()
	opts := omp(home).Field("model").Options(map[string]string{"model": "zai/glm-5"})
	byValue := map[string]Option{}
	for _, o := range opts {
		if _, dup := byValue[o.Value]; dup {
			t.Fatalf("%s offered twice: %+v", o.Value, opts)
		}
		byValue[o.Value] = o
	}
	return byValue
}

// With no answer from omp (under test there is none), the model picker
// offers the models of the providers the user added to models.yml, beside
// models.dev's for the current provider, spelled as ownOptions spells them
// (the name as the note): not magpie's own entry, nor a provider whose
// models omp only discovers at run time. A model named in both keeps what
// models.dev knows of it.
func TestOmpOwnModels(t *testing.T) {
	home := ompModelsHome(t)
	byValue := ompOptions(t, home)
	if o := byValue["codemaker/claude-opus-5-5"]; o.Note != "Claude Opus 5.5" || o.Label != "" || o.Group != "codemaker" {
		t.Fatalf("codemaker model: %+v in %+v", o, byValue)
	}
	if o, ok := byValue["codemaker/glm-5"]; !ok || o.Note != "" {
		t.Fatalf("model without a name: %+v in %+v", o, byValue)
	}
	// models.yml's zai glm-5 has no name; models.dev's has one
	if o := byValue["zai/glm-5"]; o.Note != "GLM-5" || o.Icon == "" {
		t.Fatalf("zai/glm-5 lost models.dev's name or icon: %+v in %+v", o, byValue)
	}
	// magpie has no provider here, so its entry's model comes from nowhere else
	for v := range byValue {
		if strings.HasPrefix(v, "lan/") || v == "magpie/deepseek/pro" {
			t.Fatalf("%s offered: %+v", v, byValue)
		}
	}
}

// ompAsked waits for the ask of omp under way for home, if one is.
func ompAsked(home string) {
	ompLists.Lock()
	var done chan struct{}
	if l := ompLists.m[filepath.Join(home, ".omp", "agent")]; l != nil {
		done = l.asking
	}
	ompLists.Unlock()
	if done != nil {
		<-done
	}
}

// The model picker offers what omp itself lists as available — the
// providers built into it that are signed in too — rather than models.yml;
// never magpie's entry, whose models are offered as magpie's. A model omp
// lists without a name keeps models.dev's. omp is asked behind the look:
// the first has models.yml's, and omp isn't asked again until models.yml
// changes. An omp that can't answer leaves models.yml's.
func TestOmpListedModels(t *testing.T) {
	home := ompModelsHome(t)
	out, calls := `{"models":[
{"provider":"anthropic","kind":"chat","id":"claude-opus-5","selector":"anthropic/claude-opus-5","name":"Claude Opus 5"},
{"provider":"codemaker","kind":"chat","id":"glm-5","selector":"codemaker/glm-5","name":""},
{"provider":"cursor","kind":"chat","id":"auto","selector":"cursor/auto","name":"Auto"},
{"provider":"magpie","kind":"chat","id":"deepseek/pro","selector":"magpie/deepseek/pro","name":"DeepSeek Pro"},
{"provider":"zai","kind":"chat","id":"glm-5","selector":"zai/glm-5","name":""}]}`, 0
	old := runOmpModels
	runOmpModels = func() ([]byte, error) { calls++; return []byte(out), nil }
	t.Cleanup(func() { runOmpModels = old })

	// the first look doesn't wait for omp
	byValue := ompOptions(t, home)
	if _, ok := byValue["anthropic/claude-opus-5"]; ok {
		t.Fatalf("waited for omp: %+v", byValue)
	}
	if _, ok := byValue["codemaker/claude-opus-5-5"]; !ok {
		t.Fatalf("models.yml's not offered before omp answered: %+v", byValue)
	}
	ompAsked(home)

	byValue = ompOptions(t, home)
	if o := byValue["anthropic/claude-opus-5"]; o.Note != "Claude Opus 5" || o.Group != "anthropic" {
		t.Fatalf("a provider built into omp: %+v in %+v", o, byValue)
	}
	if o, ok := byValue["codemaker/glm-5"]; !ok || o.Group != "codemaker" {
		t.Fatalf("a model without a name: %+v in %+v", o, byValue)
	}
	if _, ok := byValue["cursor/auto"]; !ok {
		t.Fatalf("cursor/auto missing: %+v", byValue)
	}
	if o := byValue["zai/glm-5"]; o.Note != "GLM-5" || o.Icon == "" {
		t.Fatalf("zai/glm-5 lost models.dev's name or icon: %+v in %+v", o, byValue)
	}
	// what omp doesn't list isn't offered, though models.yml names it
	for _, v := range []string{"magpie/deepseek/pro", "codemaker/claude-opus-5-5"} {
		if _, ok := byValue[v]; ok {
			t.Fatalf("%s offered: %+v", v, byValue)
		}
	}
	if ompAsked(home); calls != 1 {
		t.Fatalf("omp asked %d times with nothing changed", calls)
	}

	// a provider added to models.yml: what omp said is still served, and
	// omp is asked again behind it
	models := filepath.Join(home, ".omp", "agent", "models.yml")
	b, _ := os.ReadFile(models)
	os.WriteFile(models, append(b, "  added:\n    models:\n      - id: new\n"...), 0o644)
	out = strings.Replace(out, `"models":[`, `"models":[{"provider":"added","id":"new","selector":"added/new"},`, 1)
	if _, ok := ompOptions(t, home)["anthropic/claude-opus-5"]; !ok {
		t.Fatal("omp's answer dropped while it is asked again")
	}
	ompAsked(home)
	if _, ok := ompOptions(t, home)["added/new"]; !ok || calls != 2 {
		t.Fatalf("omp not asked again after models.yml changed: %d calls", calls)
	}

	// an omp that prints something else leaves models.yml's
	home = ompModelsHome(t)
	out = "not json"
	ompOptions(t, home)
	ompAsked(home)
	byValue = ompOptions(t, home)
	if _, ok := byValue["codemaker/claude-opus-5-5"]; !ok {
		t.Fatalf("models.yml's not offered: %+v", byValue)
	}
	if _, ok := byValue["anthropic/claude-opus-5"]; ok {
		t.Fatalf("an answer omp didn't give: %+v", byValue)
	}
}
