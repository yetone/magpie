package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// qwenUser is a settings.json as Qwen Code leaves it for someone with a
// custom model provider of their own, and settings magpie has nothing to
// do with.
const qwenUser = `{
  "env": {
    "IDEALAB_API_KEY": "team-key"
  },
  "modelProviders": {
    "openai": [
      {
        "id": "glm-5",
        "name": "[IdeaLab] glm-5",
        "baseUrl": "https://idealab.example.com/api/openai/v1",
        "envKey": "IDEALAB_API_KEY"
      }
    ]
  },
  "model": {
    "name": "glm-5",
    "baseUrl": "https://idealab.example.com/api/openai/v1"
  },
  "outputLanguage": "zh-CN"
}
`

func qwenHome(t *testing.T) (home, path string) {
	t.Helper()
	home = syncHome(t)
	for _, p := range []provider.Provider{
		{ID: "deepseek", Name: "DeepSeek", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"pro", "flash"}},
		{ID: "anth", Name: "Anth", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-sonnet-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	path = filepath.Join(home, ".qwen", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	return home, path
}

// qwenFile is settings.json read back: the modelProviders.openai entries
// by id, their order, and the whole file.
func qwenFile(t *testing.T, path string) (map[string]map[string]any, []string, map[string]any) {
	t.Helper()
	var f map[string]any
	if err := json.Unmarshal([]byte(readFile(path)), &f); err != nil {
		t.Fatalf("%v\n%s", err, readFile(path))
	}
	byID := map[string]map[string]any{}
	var order []string
	mp, _ := f["modelProviders"].(map[string]any)
	ms, _ := mp["openai"].([]any)
	for _, m := range ms {
		e := m.(map[string]any)
		id, _ := e["id"].(string)
		byID[id] = e
		order = append(order, id)
	}
	return byID, order, f
}

func TestQwen(t *testing.T) {
	home, path := qwenHome(t)
	os.WriteFile(path, []byte(qwenUser), 0o644)
	a, err := Find("qwen")
	if err != nil || a.Path != path {
		t.Fatalf("find: %v %v", a, err)
	}
	if !a.Detected() {
		t.Fatal("not detected with ~/.qwen")
	}
	if got := a.Values()["model"]; got != "glm-5" {
		t.Fatalf("model: %q", got)
	}
	// the picker: the user's own entries, then magpie's catalog
	opts := a.Field("model").Options(a.Values())
	if opts[0].Value != "glm-5" || opts[0].Label != "[IdeaLab] glm-5" || opts[0].Group != "Qwen Code" {
		t.Fatalf("own options: %+v", opts[:1])
	}
	var via bool
	for _, o := range opts {
		via = via || o.Value == "magpie/deepseek/pro"
	}
	if !via {
		t.Fatalf("no magpie/deepseek/pro in %+v", opts)
	}

	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	ms, order, f := qwenFile(t, path)
	if order[0] != "glm-5" {
		t.Fatalf("the user's entry moved: %v", order)
	}
	// the user's entry is as it was
	if !strings.Contains(readFile(path), qwenUser[strings.Index(qwenUser, "      {"):strings.Index(qwenUser, "      }\n")+7]) {
		t.Fatalf("user's entry rewritten:\n%s", readFile(path))
	}
	m := f["model"].(map[string]any)
	if m["name"] != "deepseek/pro" || m["baseUrl"] != gatewayV1() {
		t.Fatalf("model: %v", m)
	}
	env := f["env"].(map[string]any)
	if env["IDEALAB_API_KEY"] != "team-key" || env["MAGPIE_QWEN_API_KEY"] != gateway.Token {
		t.Fatalf("env: %v", env)
	}
	if auth := f["security"].(map[string]any)["auth"].(map[string]any)["selectedType"]; auth != "openai" {
		t.Fatalf("selectedType: %v", auth)
	}
	if f["outputLanguage"] != "zh-CN" {
		t.Fatalf("settings: %v", f)
	}
	for _, id := range []string{"deepseek/pro", "deepseek/flash", "anth/claude-sonnet-5"} {
		e := ms[id]
		if e == nil {
			t.Errorf("%s missing: %v", id, order)
			continue
		}
		if e["name"] != "magpie/"+id || e["baseUrl"] != gatewayV1() || e["envKey"] != "MAGPIE_QWEN_API_KEY" {
			t.Errorf("%s: %v", id, e)
		}
	}
	if got := a.Values()["model"]; got != "magpie/deepseek/pro" {
		t.Fatalf("model read back: %q", got)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("check: %s", d)
	}
	// another of magpie's: the stash keeps the user's own
	if err := a.Apply("model", "magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	// a provider added since joins the picker on Sync
	provider.Save(provider.Provider{ID: "later", Name: "Later", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}})
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if ms, _, _ = qwenFile(t, path); ms["later/m1"] == nil {
		t.Fatalf("sync: %v", ms)
	}
	// something else rewrote magpie's entry: its baseUrl is the one
	// before its name, as the fields are ordered
	cur := readFile(path)
	at := strings.Index(cur, `"magpie/anth/claude-sonnet-5"`)
	base := strings.LastIndex(cur[:at], gatewayV1())
	if base < 0 {
		t.Fatal("no baseUrl before the entry's name:\n" + cur)
	}
	edited := cur[:base] + "http://127.0.0.1:9/v1" + cur[base+len(gatewayV1()):]
	os.WriteFile(path, []byte(edited), 0o644)
	if d := a.Check(); !strings.Contains(d, "baseUrl") {
		t.Fatalf("check after a moved baseUrl: %q, model %q", d, a.Values()["model"])
	}
	a.Sync()
	if d := a.Check(); d != "" {
		t.Fatalf("check after sync: %s", d)
	}

	// reset: the user's model back, magpie's entries and key gone, the
	// rest as it was
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if got := readFile(path); got != qwenUser {
		t.Fatalf("not restored:\n%s", got)
	}
	if got := a.Values()["model"]; got != "glm-5" {
		t.Fatalf("reset model: %q", got)
	}
	_ = home
}

// Picking one of the user's own models while on magpie's takes magpie out.
func TestQwenOwnModel(t *testing.T) {
	_, path := qwenHome(t)
	os.WriteFile(path, []byte(qwenUser), 0o644)
	a := qwen(os.Getenv("HOME"))
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "glm-5"); err != nil {
		t.Fatal(err)
	}
	ms, order, f := qwenFile(t, path)
	if len(order) != 1 || ms["glm-5"] == nil {
		t.Fatalf("models: %v", order)
	}
	m := f["model"].(map[string]any)
	if m["name"] != "glm-5" || m["baseUrl"] != "https://idealab.example.com/api/openai/v1" {
		t.Fatalf("model: %v", m)
	}
	env := f["env"].(map[string]any)
	if _, ok := env["MAGPIE_QWEN_API_KEY"]; ok {
		t.Fatalf("key left: %v", env)
	}
	// and a reset after that leaves the user's choice, not the stash
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	a.Apply("model", "")
	if got := a.Values()["model"]; got != "glm-5" {
		t.Fatalf("reset: %q", got)
	}
}

// No settings.json at all: magpie adds only its own, and a reset leaves
// the file as empty as it began.
func TestQwenFresh(t *testing.T) {
	home, path := qwenHome(t)
	a := qwen(home)
	if a.Values()["model"] != "" {
		t.Fatal("model without a file")
	}
	if err := a.Apply("model", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if _, order, f := qwenFile(t, path); len(order) != 4 {
		t.Fatalf("models: %v", order)
	} else if env := f["env"].(map[string]any); env["MAGPIE_QWEN_API_KEY"] != gateway.Token {
		t.Fatalf("env: %v", env)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(readFile(path)); got != "{}" {
		t.Fatalf("left: %s", got)
	}
	// a file Qwen Code never put a modelProviders in reads as no models
	// of anyone at all, and magpie touches none of it
	os.WriteFile(path, []byte("{\n  \"fastModel\": \"qwen3.8-flash\"\n}\n"), 0o644)
	if got := a.Values()["model"]; got != "" {
		t.Fatalf("model without modelProviders: %q", got)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if got := readFile(path); got != "{\n  \"fastModel\": \"qwen3.8-flash\"\n}\n" {
		t.Fatalf("nothing of magpie's to take out:\n%s", got)
	}
}

// A model the user picked without a matching entry keeps no baseUrl;
// one of the user's under magpie's own env key name, and their auth
// type, are stashed and put back when magpie steps out.
func TestQwenBareModelAndOwnEnvKey(t *testing.T) {
	_, path := qwenHome(t)
	bare := `{
  "env": {
    "MAGPIE_QWEN_API_KEY": "the-user's-own"
  },
  "security": {
    "auth": {
      "selectedType": "qwen-oauth"
    }
  },
  "model": {
    "name": "qwen3.8-max"
  }
}
`
	os.WriteFile(path, []byte(bare), 0o644)
	a := qwen(os.Getenv("HOME"))
	if got := a.Values()["model"]; got != "qwen3.8-max" {
		t.Fatalf("bare model: %q", got)
	}
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if _, _, f := qwenFile(t, path); f["env"].(map[string]any)["MAGPIE_QWEN_API_KEY"] != gateway.Token {
		t.Fatalf("key not magpie's: %v", f["env"])
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	json.Unmarshal([]byte(readFile(path)), &f)
	if f["env"].(map[string]any)["MAGPIE_QWEN_API_KEY"] != "the-user's-own" {
		t.Fatalf("key not restored: %v", f["env"])
	}
	if got := f["security"].(map[string]any)["auth"].(map[string]any)["selectedType"]; got != "qwen-oauth" {
		t.Fatalf("auth not restored: %v", got)
	}
	m, ok := f["model"].(map[string]any)
	if !ok || m["name"] != "qwen3.8-max" {
		t.Fatalf("model not restored: %v", m)
	}
	if _, ok = m["baseUrl"]; ok {
		t.Fatalf("baseUrl it never had: %v", m)
	}
}

// An auth type of "openai" the user set themselves is told from
// magpie's, and stays when magpie steps out.
func TestQwenAuthOpenAIOfTheUsersOwn(t *testing.T) {
	_, path := qwenHome(t)
	os.WriteFile(path, []byte(`{
  "security": {
    "auth": {
      "selectedType": "openai"
    }
  },
  "model": {
    "name": "qwen3.8-max"
  }
}
`), 0o644)
	a := qwen(os.Getenv("HOME"))
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	json.Unmarshal([]byte(readFile(path)), &f)
	if got := f["security"].(map[string]any)["auth"].(map[string]any)["selectedType"]; got != "openai" {
		t.Fatalf("the user's own openai came off: %v", f)
	}
}

// $QWEN_HOME moves the config folder Qwen Code looks at.
func TestQwenHomeOverride(t *testing.T) {
	home, _ := qwenHome(t)
	other := filepath.Join(home, "elsewhere")
	t.Setenv("QWEN_HOME", other)
	a := qwen(home)
	if a.Dir != other || a.Path != filepath.Join(other, "settings.json") {
		t.Fatalf("override: %s %s", a.Dir, a.Path)
	}
}
