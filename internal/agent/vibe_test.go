package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// vibeConfig is a config.toml as Mistral Vibe 3.1 keeps it: its own model
// and provider entries, a comment the user added by hand.
const vibeConfig = `# my own pick
active_model = "devstral-2"
vim_keybindings = true

[[providers]]
name = "mistral"
api_base = "https://api.mistral.ai/v1"
api_key_env_var = "MISTRAL_API_KEY"
backend = "mistral"

[[models]]
name = "mistral-vibe-cli-latest"
provider = "mistral"
alias = "devstral-2"
`

func vibeHome(t *testing.T, body string) (home, path string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("VIBE_HOME", "")
	if err := provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Key: "k",
		Chat: "https://api.moonshot.cn/v1", Models: []string{"k2", "k2-vision"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("kimi", "https://api.moonshot.cn/v1", []catalog.Model{
		{ID: "k2", Context: 262144, Efforts: []string{"low", "medium", "high"}},
		{ID: "k2-vision", Images: true},
	}); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(home, ".vibe", "config.toml")
	if body != "" {
		writeFile(t, path, body)
	}
	return home, path
}

func vibeParse(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := toml.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s is not TOML: %v\n%s", path, err, b)
	}
	return m
}

func vibeMagpieModels(t *testing.T, m map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	list, _ := m["models"].([]any)
	for _, x := range list {
		e := x.(map[string]any)
		if e["provider"] == "magpie" {
			out[e["name"].(string)] = e
		}
	}
	return out
}

// Connecting Vibe adds the provider magpie — the gateway, with the key
// naming Vibe as a header, since Vibe takes keys from the environment only —
// and a model entry for each of magpie's models, and starts Vibe on the one
// picked. The user's own entries and pick stay, and Disconnect gives the file
// back byte for byte.
func TestVibe(t *testing.T) {
	home, path := vibeHome(t, vibeConfig)
	a := vibe(home)
	if a.Path != path || a.ID != "vibe" {
		t.Fatalf("path %q id %q", a.Path, a.ID)
	}
	f := a.Field("model")
	if got := f.Get(); got != "devstral-2" || a.Wired() || a.Check() != "" {
		t.Fatalf("before: %q wired %v check %q", got, a.Wired(), a.Check())
	}
	if err := a.Pick("model", "magpie/kimi/k2"); err != nil {
		t.Fatal(err)
	}
	m := vibeParse(t, path)
	if m["active_model"] != "magpie/kimi/k2" || m["vim_keybindings"] != true {
		t.Fatalf("top level: %v", m)
	}
	var prov map[string]any
	for _, x := range m["providers"].([]any) {
		if p := x.(map[string]any); p["name"] == "magpie" {
			prov = p
		}
	}
	gw := here(home)
	hdr, _ := prov["extra_headers"].(map[string]any)
	if prov["api_base"] != gw.v1() || prov["api_style"] != "openai" || prov["backend"] != "generic" ||
		hdr["Authorization"] != "Bearer "+agentKeyAt("vibe", gw.gw()) || prov["api_key_env_var"] != nil {
		t.Fatalf("magpie's provider: %v", prov)
	}
	models := vibeMagpieModels(t, m)
	k2, vision := models["kimi/k2"], models["kimi/k2-vision"]
	if len(models) != 2 || k2 == nil || vision == nil {
		t.Fatalf("models: %v", models)
	}
	if k2["alias"] != "magpie/kimi/k2" || k2["max_context_length"] != int64(262144) ||
		!reflect.DeepEqual(k2["thinking_levels"], []any{"off", "low", "medium", "high"}) {
		t.Fatalf("k2: %v", k2)
	}
	if vision["supports_images"] != true || !reflect.DeepEqual(vision["thinking_levels"], []any{"off"}) ||
		vision["max_context_length"] == nil {
		t.Fatalf("k2-vision: %v", vision)
	}
	if !a.Wired() || f.Get() != "magpie/kimi/k2" {
		t.Fatalf("wired %v, model %q", a.Wired(), f.Get())
	}
	if c := a.Check(); c != "" {
		t.Fatalf("check: %s", c)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "# my own pick\n") || !strings.Contains(string(b), `api_key_env_var = "MISTRAL_API_KEY"`) {
		t.Fatalf("the user's own lines changed:\n%s", b)
	}

	// the key going missing from the header is said
	writeFile(t, path, strings.Replace(string(b), "Bearer "+agentKeyAt("vibe", gw.gw()), "Bearer nope", 1))
	if c := a.Check(); c == "" {
		t.Fatal("a changed key wasn't noticed")
	}
	writeFile(t, path, string(b))

	if err := a.Pick("model", "magpie/kimi/k2-vision"); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != vibeConfig {
		t.Fatalf("Disconnect left:\n%s", got)
	}
	if a.Wired() {
		t.Fatal("still wired")
	}
}

// A model of Vibe's own picked while it is on magpie takes magpie out.
func TestVibeOwnModelTakesMagpieOut(t *testing.T) {
	home, path := vibeHome(t, vibeConfig)
	a := vibe(home)
	if err := a.Pick("model", "magpie/kimi/k2"); err != nil {
		t.Fatal(err)
	}
	if err := a.Pick("model", "devstral-2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != vibeConfig {
		t.Fatalf("got:\n%s", got)
	}
	if a.Wired() {
		t.Fatal("still wired")
	}
}

// A thinking level the user chose on a magpie model stays through a catalog
// sync; a model magpie lists no more leaves Vibe's list; a sync with nothing
// to change doesn't write.
func TestVibeSyncKeepsTheirs(t *testing.T) {
	home, path := vibeHome(t, "")
	a := vibe(home)
	if err := a.Pick("model", "magpie/kimi/k2"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	writeFile(t, path, strings.Replace(string(b), `alias = "magpie/kimi/k2"`, `alias = "magpie/kimi/k2"`+"\nthinking = \"high\"", 1))
	if err := catalog.SaveLive("kimi", "https://api.moonshot.cn/v1", []catalog.Model{
		{ID: "k2", Context: 262144, Efforts: []string{"low", "medium", "high"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Key: "k",
		Chat: "https://api.moonshot.cn/v1", Models: []string{"k2"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	models := vibeMagpieModels(t, vibeParse(t, path))
	if len(models) != 1 || models["kimi/k2"]["thinking"] != "high" {
		t.Fatalf("models: %v", models)
	}
	before, _ := os.ReadFile(path)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatalf("a sync with nothing to change wrote:\n%s", after)
	}
}

// Vibe's folder is $VIBE_HOME when it is set, and a ~ in it is the home; a
// file that isn't TOML is left as it is.
func TestVibeHome(t *testing.T) {
	home, _ := vibeHome(t, "")
	t.Setenv("VIBE_HOME", "~/elsewhere")
	a := vibe(home)
	if a.Dir != filepath.Join(home, "elsewhere") || a.Path != filepath.Join(home, "elsewhere", "config.toml") {
		t.Fatalf("dir %q path %q", a.Dir, a.Path)
	}
	writeFile(t, a.Path, "this = [is not\n")
	if err := a.Pick("model", "magpie/kimi/k2"); err == nil {
		t.Fatal("a broken file was written over")
	}
	if got, _ := os.ReadFile(a.Path); string(got) != "this = [is not\n" {
		t.Fatalf("file changed: %q", got)
	}
}

// Avibe's `vibe` command isn't Mistral Vibe's.
func TestVibeNotAvibe(t *testing.T) {
	if !vibeNotAvibe("/home/u/.local/share/uv/tools/avibe/bin/vibe") || vibeNotAvibe("/home/u/.local/bin/vibe") {
		t.Fatal("binNot judged wrongly")
	}
}
