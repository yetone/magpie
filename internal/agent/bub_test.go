package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// bubConfig is a config.yml as Bub 0.6.2 keeps it after the user named a
// provider of their own: its model, an entry under providers, a key of a
// plugin's, and a comment the user added by hand.
const bubConfig = `# my own pick
model: openrouter:qwen/qwen3-coder
providers:
  openrouter:
    type: openrouter
    api_key: sk-or-mine
max_steps: 80
`

func bubHome(t *testing.T, body string) (home, path string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("BUB_HOME", "")
	if err := provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Key: "k",
		Chat: "https://api.moonshot.cn/v1", Models: []string{"k2", "k2-vision"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("kimi", "https://api.moonshot.cn/v1", []catalog.Model{
		{ID: "k2", Context: 262144}, {ID: "k2-vision", Images: true},
	}); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(home, ".bub", "config.yml")
	if body != "" {
		writeFile(t, path, body)
	}
	return home, path
}

func bubParse(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s is not YAML: %v\n%s", path, err, b)
	}
	return m
}

// Connecting Bub adds the provider magpie — Republic's magpie type at the
// gateway, with the key naming Bub — and points model at
// magpie:<provider>/<model>. The user's own provider, keys and comment stay,
// and Disconnect gives the file back byte for byte.
func TestBub(t *testing.T) {
	home, path := bubHome(t, bubConfig)
	a := bub(home)
	if a.Path != path || a.ID != "bub" || a.Dir != filepath.Join(home, ".bub") {
		t.Fatalf("path %q dir %q id %q", a.Path, a.Dir, a.ID)
	}
	f := a.Field("model")
	if got := f.Get(); got != "openrouter:qwen/qwen3-coder" || a.Wired() || a.Check() != "" {
		t.Fatalf("before: %q wired %v check %q", got, a.Wired(), a.Check())
	}
	if err := a.Pick("model", "magpie/kimi/k2"); err != nil {
		t.Fatal(err)
	}
	m := bubParse(t, path)
	gw := here(home)
	provs, _ := m["providers"].(map[string]any)
	mine, _ := provs["magpie"].(map[string]any)
	if m["model"] != "magpie:kimi/k2" || m["max_steps"] != 80 {
		t.Fatalf("top level: %v", m)
	}
	if mine["type"] != "magpie" || mine["api_base"] != gw.v1() || mine["api_key"] != agentKeyAt("bub", gw.gw()) {
		t.Fatalf("magpie's provider: %v", mine)
	}
	if or, _ := provs["openrouter"].(map[string]any); or["api_key"] != "sk-or-mine" {
		t.Fatalf("the user's provider: %v", provs)
	}
	if !a.Wired() || f.Get() != "magpie/kimi/k2" {
		t.Fatalf("wired %v, model %q", a.Wired(), f.Get())
	}
	if c := a.Check(); c != "" {
		t.Fatalf("check: %s", c)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "# my own pick\n") {
		t.Fatalf("the user's comment went:\n%s", b)
	}

	// the key changed by hand is said
	writeFile(t, path, strings.Replace(string(b), agentKeyAt("bub", gw.gw()), "nope", 1))
	if c := a.Check(); c == "" {
		t.Fatal("a changed key wasn't noticed")
	}
	writeFile(t, path, string(b))

	if err := a.Pick("model", "magpie/kimi/k2-vision"); err != nil {
		t.Fatal(err)
	}
	if got := bubParse(t, path)["model"]; got != "magpie:kimi/k2-vision" {
		t.Fatalf("model %v", got)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != bubConfig {
		t.Fatalf("Disconnect left:\n%s", got)
	}
	if a.Wired() {
		t.Fatal("still wired")
	}
}

// Bub with no config.yml yet (it runs on its default model): connecting
// writes the file, and Disconnect takes magpie's keys out again, the model
// with them, so Bub is back on its default.
func TestBubNoConfig(t *testing.T) {
	home, path := bubHome(t, "")
	a := bub(home)
	if err := a.Pick("model", "magpie/kimi/k2"); err != nil {
		t.Fatal(err)
	}
	if m := bubParse(t, path); m["model"] != "magpie:kimi/k2" {
		t.Fatalf("got %v", m)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	m := bubParse(t, path)
	if len(m) != 0 {
		t.Fatalf("left %v", m)
	}
}

// A model of Bub's own picked while it is on magpie takes magpie out.
func TestBubOwnModelTakesMagpieOut(t *testing.T) {
	home, path := bubHome(t, bubConfig)
	a := bub(home)
	if err := a.Pick("model", "magpie/kimi/k2"); err != nil {
		t.Fatal(err)
	}
	if err := a.Pick("model", "openrouter:qwen/qwen3-coder"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != bubConfig {
		t.Fatalf("got:\n%s", got)
	}
	if a.Wired() {
		t.Fatal("still wired")
	}
}

// A provider the user had named magpie themselves (another magpie, say) is
// theirs: magpie's entry takes its place while connected, and Disconnect
// puts theirs back, headers and all.
func TestBubKeepsTheirMagpieEntry(t *testing.T) {
	const theirs = `model: openai:gpt-5
providers:
  magpie:
    type: openai
    api_base: https://their.example/v1
    api_key: theirs
    headers:
      X-Team: a
`
	home, path := bubHome(t, theirs)
	a := bub(home)
	if a.Wired() {
		t.Fatal("their own entry, not on a model of it, counted as wired")
	}
	if err := a.Pick("model", "magpie/kimi/k2"); err != nil {
		t.Fatal(err)
	}
	mine := bubParse(t, path)["providers"].(map[string]any)["magpie"].(map[string]any)
	if mine["api_base"] != here(home).v1() || mine["type"] != "magpie" {
		t.Fatalf("magpie's entry: %v", mine)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != theirs {
		t.Fatalf("Disconnect left:\n%s", got)
	}
}

// The gateway moving (another port) is followed by a sync while magpie is
// on, and a sync with nothing to change doesn't write.
func TestBubSync(t *testing.T) {
	home, path := bubHome(t, bubConfig)
	a := bub(home)
	if err := a.Pick("model", "magpie/kimi/k2"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	writeFile(t, path, strings.Replace(string(b), here(home).v1(), "http://127.0.0.1:1/v1", 1))
	if a.Check() == "" {
		t.Fatal("a moved gateway wasn't noticed")
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(b) || a.Check() != "" {
		t.Fatalf("sync left:\n%s", got)
	}
	st, _ := os.Stat(path)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if st2, _ := os.Stat(path); !st2.ModTime().Equal(st.ModTime()) {
		t.Fatal("a sync with nothing to change wrote")
	}
}

// Bub's folder is $BUB_HOME when it is set, and a ~ in it is the home; a
// file that isn't YAML is left as it is.
func TestBubHome(t *testing.T) {
	home, _ := bubHome(t, "")
	t.Setenv("BUB_HOME", "~/elsewhere")
	a := bub(home)
	if a.Dir != filepath.Join(home, "elsewhere") || a.Path != filepath.Join(home, "elsewhere", "config.yml") {
		t.Fatalf("dir %q path %q", a.Dir, a.Path)
	}
	writeFile(t, a.Path, "model: [is not\n")
	if err := a.Pick("model", "magpie/kimi/k2"); err == nil {
		t.Fatal("a broken file was written over")
	}
	if got, _ := os.ReadFile(a.Path); string(got) != "model: [is not\n" {
		t.Fatalf("file changed: %q", got)
	}
	abs := filepath.Join(home, "abs")
	t.Setenv("BUB_HOME", abs)
	if a := bub(home); a.Dir != abs {
		t.Fatalf("dir %q", a.Dir)
	}
}

// Bub 0.5, on any-llm, has no magpie provider type: its entry is openai.
// Bub 0.6's Republic has one; a bub whose packages can't be read gets it.
func TestBubType(t *testing.T) {
	venv := func(pkgs ...string) string {
		root := t.TempDir()
		site := filepath.Join(root, "lib", "python3.12", "site-packages")
		for _, p := range pkgs {
			writeFile(t, filepath.Join(site, filepath.FromSlash(p)), "")
		}
		writeFile(t, filepath.Join(root, "bin", "bub"), "")
		return filepath.Join(root, "bin", "bub")
	}
	for _, c := range []struct {
		bin, want string
	}{
		{venv("republic/providers/magpie.py", "any_llm/__init__.py"), "magpie"},
		{venv("any_llm/__init__.py"), "openai"},
		{venv(), "magpie"},
		{"", "magpie"},
	} {
		if got := bubType(c.bin); got != c.want {
			t.Errorf("%s: %q, want %q", c.bin, got, c.want)
		}
	}
}
