//go:build !windows

package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/update"
)

// agyPluginSrc is @magpie-community/agent-agy's agy.agent.js, as published
// (magpie-community/plugins, packages/agent-agy).
const agyPluginSrc = `
// Antigravity CLI (agy) keeps its settings in
// ~/.gemini/antigravity-cli/settings.json, and the models it can pick in
// customModelsConfig.customModels, each {apiProvider, modelName}. It takes
// the address and key of a Gemini endpoint only from how it is started,
// so magpie starts it: the function agy magpie puts in your shells runs
// ` + "`" + `magpie agy run` + "`" + `, which gives agy GEMINI_API_KEY, GOOGLE_GEMINI_BASE_URL,
// --settings magpie-settings.json and --model while its model is one of
// magpie's.
//
// magpie writes only magpie-settings.json, a file of its own beside
// settings.json, which agy reads for the one session started with
// --settings: "modelProvider": "gemini" in settings.json would make every
// agy an API-key session, with no Google account and so no remote
// control. agy takes customModels whole from that file, so the user's own
// custom models are carried into it beside magpie's. remote-control runs
// as agy is, on the user's sign-in.
export const agent = {
  id: "agy",
  name: "Antigravity CLI",
  icon: "antigravity-color",
  aliases: ["antigravity-cli"],
  bin: "agy",
  dir: "~/.gemini/antigravity-cli",
  config: "~/.gemini/antigravity-cli/magpie-settings.json",
  base: "~/.gemini/antigravity-cli/settings.json",
  format: "json",
  model: "model",
  prefix: "magpie/",
  launch: {
    env: { GEMINI_API_KEY: "{key}", GOOGLE_GEMINI_BASE_URL: "{url}" },
    model: "--model",
    args: { "--settings": "{config}" },
    plain: ["remote-control", "--remote-control", "-remote-control"],
  },
  effort: true,
  wsl: true,
}

const GEMINI = "API_PROVIDER_GOOGLE_GEMINI"

// the user's own custom models, from settings.json
const ownModels = (base) => {
  const out = {}
  for (const [k, v] of Object.entries(base?.customModelsConfig?.customModels ?? {})) {
    if (!k.startsWith("magpie/")) out[k] = v
  }
  return out
}

export function connect({ models, base }) {
  const custom = ownModels(base)
  for (const m of models) custom[` + "`" + `magpie/${m.id}` + "`" + `] = { apiProvider: GEMINI, modelName: m.id }
  return { modelProvider: "gemini", "customModelsConfig.customModels": custom }
}

// the user's own models, which go to their provider rather than through
// magpie, and the one agy is on when it isn't listed
export function own({ base, model }) {
  const custom = base?.customModelsConfig?.customModels ?? {}
  const out = []
  for (const [k, v] of Object.entries(custom)) {
    if (k.startsWith("magpie/")) continue
    const p = String(v?.apiProvider ?? "")
    const direct = p.includes("ANTHROPIC") ? "Anthropic" : p.includes("OPENAI") ? "OpenAI" : "Google"
    out.push({ value: k, model: String(v?.modelName ?? k), direct })
  }
  out.sort((a, b) => (a.value < b.value ? -1 : a.value > b.value ? 1 : 0))
  if (model && !(model in custom)) out.unshift({ value: model, model, direct: "Google" })
  return out
}
`

// agyPluginHome is a home with the agent-agy plugin installed, agentMovers
// naming it for agy, shells' profiles with a line of the user's own, and
// agy's settings.json as settings.
func agyPluginHome(t *testing.T, settings string) (home, path, mine string, rcs []string) {
	t.Helper()
	home = pluggedHome(t, agyPluginSrc)
	t.Setenv("ZDOTDIR", "")
	mine = "alias ll='ls -l'\n"
	rcs = []string{".zshrc", ".bashrc", ".bash_profile"}
	for _, n := range rcs {
		os.WriteFile(filepath.Join(home, n), []byte(mine), 0o644)
	}
	path = filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	if settings != "" {
		os.WriteFile(path, []byte(settings), 0o644)
	}
	return
}

// overOf is magpie's own settings file beside settings.json at path.
func overOf(path string) string { return filepath.Join(filepath.Dir(path), agyOverride) }

func fileGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s is still there: %v", path, err)
	}
}

// moveAgy names the plugin installed in home as the one agy moves onto.
func moveAgy(t *testing.T, home string) {
	was := agentMovers
	agentMovers = map[string]string{"agy": filepath.Join(home, "plugins", "pa")}
	t.Cleanup(func() { agentMovers = was })
}

func settingsOf(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return v
}

func profilesAre(t *testing.T, home, mine string, rcs []string) {
	t.Helper()
	for _, n := range rcs {
		if b, _ := os.ReadFile(filepath.Join(home, n)); string(b) != mine {
			t.Fatalf("%s:\n%s", n, b)
		}
	}
}

// agy as the agent-agy plugin does what the built-in did: one agy in the
// list, found by its alias too; the user's own custom models offered as
// going to their provider; magpie's model written with modelProvider and
// magpie's custom models beside the user's; the function agy in every
// shell, which starts it with the variables and --model; and its own model
// again, or Disconnect, puts settings.json and the profiles back as they
// were.
func TestAgyPlugin(t *testing.T) {
	const before = `{"model": "gemini-3-pro", "customModelsConfig": {"customModels": {"my-claude": {"apiProvider": "API_PROVIDER_ANTHROPIC", "modelName": "claude-sonnet-4-5"}}}}`
	home, path, mine, rcs := agyPluginHome(t, before)
	moveAgy(t, home)
	var want map[string]any
	json.Unmarshal([]byte(before), &want)

	n := 0
	for _, a := range All() {
		if a.ID == "agy" {
			n++
		}
	}
	a, err := Find("agy")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || a.Plugin == "" || a.Name != "Antigravity CLI" {
		t.Fatalf("%d agy, %+v", n, a)
	}
	if b, _ := Find("antigravity-cli"); b == nil || b.Plugin != a.Plugin {
		t.Fatal("not found by its alias")
	}
	f := a.Field("model")
	opts := f.Options(map[string]string{"model": f.Get()})
	direct := map[string]string{}
	for _, o := range opts {
		direct[o.Value] = o.Direct
	}
	if direct["my-claude"] != "Anthropic" || direct["gemini-3-pro"] != "Google" || !containsOption(opts, "magpie/deepseek/pro") {
		t.Fatalf("options: %+v", opts)
	}
	if a.Field("effort") == nil {
		t.Fatal("no effort field")
	}

	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	// settings.json is left as it was; magpie's file has the model, the
	// provider and every custom model, the user's own carried in
	if c := settingsOf(t, path); !reflect.DeepEqual(c, want) {
		t.Fatalf("settings.json: %v", c)
	}
	over := overOf(path)
	c := settingsOf(t, over)
	custom, _ := c["customModelsConfig"].(map[string]any)["customModels"].(map[string]any)
	if c["model"] != "magpie/deepseek/pro" || c["modelProvider"] != "gemini" || custom["my-claude"] == nil ||
		!reflect.DeepEqual(custom["magpie/deepseek/pro"], map[string]any{"apiProvider": "API_PROVIDER_GOOGLE_GEMINI", "modelName": "deepseek/pro"}) ||
		custom["magpie/deepseek/flash"] == nil {
		t.Fatalf("wired: %v", c)
	}
	if f.Get() != "magpie/deepseek/pro" || a.Check() != "" {
		t.Fatalf("get %q, check %q", f.Get(), a.Check())
	}
	s := a.Start()
	if s.Bin != "agy" || s.Flag != "--model" || s.Model != "magpie/deepseek/pro" ||
		!slices.Equal(s.Env, []string{"GEMINI_API_KEY=" + gateway.TokenFor("agy"), "GOOGLE_GEMINI_BASE_URL=" + gateway.URL()}) ||
		!slices.Equal(s.Args, []string{"--settings", over}) {
		t.Fatalf("start: %+v", s)
	}
	if !s.Given("-model=x") || s.Given("--modelx") {
		t.Fatal("Given")
	}
	// remote-control runs as agy is, on the user's Google sign-in
	for _, arg := range []string{"remote-control", "--remote-control", "-remote-control=x"} {
		if !s.Plained(arg) {
			t.Fatalf("%s is not plain", arg)
		}
	}
	if s.Plained("--model") || s.Plained("remote") {
		t.Fatal("Plained took another")
	}
	if st := Starter("antigravity-cli"); st == nil || st.Plugin != a.Plugin {
		t.Fatal("Starter by alias")
	}
	if Starter("ag") != nil {
		t.Fatal("Starter took a prefix")
	}
	in := update.ShellFunctionIn("agy")
	if len(in) == 0 {
		t.Fatal("no shell's profile has agy")
	}
	for _, p := range in {
		b, _ := os.ReadFile(p)
		if s := string(b); !strings.HasPrefix(s, mine+"\n# >>> magpie agy >>>\n"+agyPosixHead) || !strings.Contains(s, ` agy run "$@"; else command agy "$@"; fi`) {
			t.Fatalf("%s:\n%s", p, s)
		}
	}
	if a.Typed() != "agy" || !strings.HasPrefix(a.Notice(), "Type agy in a new terminal: magpie added it to ~/") {
		t.Fatalf("typed %q, notice %q", a.Typed(), a.Notice())
	}
	if want := "GEMINI_API_KEY=" + gateway.TokenFor("agy") + " GOOGLE_GEMINI_BASE_URL=" + gateway.URL() + " agy --settings " + posixQuote(over) + " --model 'magpie/deepseek/pro'"; a.Launch() != want {
		t.Fatalf("launch %q, want %q", a.Launch(), want)
	}
	// another of magpie's models
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if a.Start().Model != "magpie/deepseek/flash" {
		t.Fatal("flash")
	}

	// its own again: magpie's file gone, settings.json as it was
	if err := f.Set("gemini-3-pro"); err != nil {
		t.Fatal(err)
	}
	fileGone(t, over)
	if c := settingsOf(t, path); !reflect.DeepEqual(c, want) {
		t.Fatalf("own: %v", c)
	}
	if f.Get() != "gemini-3-pro" {
		t.Fatalf("get off magpie %q", f.Get())
	}
	profilesAre(t, home, mine, rcs)
	if s := a.Start(); s.Model != "" || s.Env != nil || s.Args != nil {
		t.Fatalf("start off magpie: %+v", s)
	}
	// another of the user's own, written into settings.json
	if err := f.Set("my-claude"); err != nil {
		t.Fatal(err)
	}
	if c := settingsOf(t, path); c["model"] != "my-claude" {
		t.Fatalf("own model: %v", c)
	}
	fileGone(t, over)
	os.WriteFile(path, []byte(before), 0o644)

	// Disconnect: magpie's file gone, settings.json untouched
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Unwire(); err != nil {
		t.Fatal(err)
	}
	fileGone(t, over)
	if c := settingsOf(t, path); !reflect.DeepEqual(c, want) {
		t.Fatalf("unwired: %v", c)
	}
	profilesAre(t, home, mine, rcs)
}

// A user the built-in agy wired keeps their setup when agy moves onto the
// plugin: the plugin's agy reads as connected on magpie's model, started
// with magpie's file, and its own model again takes magpie's file and the
// function away, settings.json as the user had it. With the plugin gone,
// the built-in is agy again.
func TestAgyHandedToPlugin(t *testing.T) {
	const before = `{"model": "gemini-3-pro", "modelProvider": "vertex", "customModelsConfig": {"customModels": {"mine": {"apiProvider": "API_PROVIDER_GOOGLE_GEMINI", "modelName": "gemini-3-pro"}}}}`
	home, path, mine, rcs := agyPluginHome(t, before)
	var want map[string]any
	json.Unmarshal([]byte(before), &want)

	b, err := Find("agy")
	if err != nil || b.Plugin != "" {
		t.Fatalf("built-in: %+v %v", b, err)
	}
	if err := b.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if len(update.ShellFunctionIn("agy")) == 0 {
		t.Fatal("the built-in gave no function")
	}

	moveAgy(t, home)
	a, err := Find("agy")
	if err != nil || a.Plugin == "" {
		t.Fatalf("moved: %+v %v", a, err)
	}
	f := a.Field("model")
	if f.Get() != "magpie/deepseek/pro" || a.Check() != "" || a.Start().Model != "magpie/deepseek/pro" {
		t.Fatalf("handed over: get %q, check %q", f.Get(), a.Check())
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if c := settingsOf(t, path); !reflect.DeepEqual(c, want) {
		t.Fatalf("settings.json after sync: %v", c)
	}
	if err := f.Set("gemini-3-pro"); err != nil {
		t.Fatal(err)
	}
	fileGone(t, overOf(path))
	if c := settingsOf(t, path); !reflect.DeepEqual(c, want) {
		t.Fatalf("own: %v", c)
	}
	profilesAre(t, home, mine, rcs)

	// the plugin gone: the built-in again
	agentMovers = map[string]string{}
	if b, _ := Find("agy"); b == nil || b.Plugin != "" {
		t.Fatalf("after: %+v", b)
	}
}

// A settings.json an older built-in wired (model, modelProvider and
// magpie's custom models in it, the user's own stashed) is given back when
// the plugin takes agy over, and magpie's model moves into magpie's file.
func TestAgyOldWiringHandedToPlugin(t *testing.T) {
	const old = `{"model": "magpie/deepseek/pro", "modelProvider": "gemini", "theme": "dark", "customModelsConfig": {"customModels": {"mine": {"apiProvider": "API_PROVIDER_ANTHROPIC", "modelName": "claude-sonnet-4-5"}, "magpie/deepseek/pro": {"apiProvider": "API_PROVIDER_GOOGLE_GEMINI", "modelName": "deepseek/pro"}}}}`
	home, path, mine, rcs := agyPluginHome(t, old)
	stash(map[string]string{"agy:" + path + ":model": "gemini-3-pro", "agy:" + path + ":modelProvider": "vertex"})
	moveAgy(t, home)
	a, _ := Find("agy")
	if a == nil || a.Plugin == "" {
		t.Fatal("not moved")
	}
	f := a.Field("model")
	if f.Get() != "magpie/deepseek/pro" || a.Check() != "" {
		t.Fatalf("handed over: get %q, check %q", f.Get(), a.Check())
	}
	want := map[string]any{"model": "gemini-3-pro", "modelProvider": "vertex", "theme": "dark", "customModelsConfig": map[string]any{"customModels": map[string]any{"mine": map[string]any{"apiProvider": "API_PROVIDER_ANTHROPIC", "modelName": "claude-sonnet-4-5"}}}}
	if c := settingsOf(t, path); !reflect.DeepEqual(c, want) {
		t.Fatalf("settings.json given back: %v", c)
	}
	c := settingsOf(t, overOf(path))
	custom, _ := c["customModelsConfig"].(map[string]any)["customModels"].(map[string]any)
	if c["model"] != "magpie/deepseek/pro" || c["modelProvider"] != "gemini" || custom["mine"] == nil {
		t.Fatalf("magpie's file: %v", c)
	}
	if err := a.Unwire(); err != nil {
		t.Fatal(err)
	}
	fileGone(t, overOf(path))
	if c := settingsOf(t, path); !reflect.DeepEqual(c, want) {
		t.Fatalf("unwired: %v", c)
	}
	profilesAre(t, home, mine, rcs)
}

// A user the built-in wired with no settings.json of their own: Disconnect
// on the plugin takes magpie's file away and makes no settings.json.
func TestAgyHandedOverUnwire(t *testing.T) {
	home, path, mine, rcs := agyPluginHome(t, "")
	b, _ := Find("agy")
	if err := b.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	moveAgy(t, home)
	a, _ := Find("agy")
	if a.Plugin == "" {
		t.Fatal("not moved")
	}
	if err := a.Unwire(); err != nil {
		t.Fatal(err)
	}
	fileGone(t, overOf(path))
	fileGone(t, path)
	profilesAre(t, home, mine, rcs)
}
