package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func TestAgy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	// the user's own: a model of theirs on their own Gemini key, and a
	// setting magpie has nothing to do with
	orig := `{
  "customModelsConfig": {
    "customModels": {
      "mine": {
        "apiProvider": "API_PROVIDER_GOOGLE_GEMINI",
        "modelName": "gemini-3-pro"
      }
    }
  },
  "model": "mine",
  "modelProvider": "vertex",
  "trustedWorkspaces": ["/w"]
}
`
	os.WriteFile(path, []byte(orig), 0o644)
	read := func() map[string]any {
		t.Helper()
		b, _ := os.ReadFile(path)
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		return m
	}
	a, err := Find("agy")
	if err != nil || a.Path != path {
		t.Fatalf("find: %v %v", a, err)
	}
	if a.Launch() != "" {
		t.Fatalf("launch while on the user's own model: %q", a.Launch())
	}
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	// agy's own settings.json is the user's, untouched: with modelProvider
	// gemini in it every agy runs on an API key and has no Google account,
	// so no remote control (baysonfox on X)
	var was map[string]any
	json.Unmarshal([]byte(orig), &was)
	if agyJSON(read()) != agyJSON(was) {
		t.Fatalf("settings.json written:\n%s", agyRead(t, path))
	}
	over := filepath.Join(filepath.Dir(path), "magpie-settings.json")
	var m map[string]any
	if err := json.Unmarshal([]byte(agyRead(t, over)), &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "magpie/deepseek/pro" || m["modelProvider"] != "gemini" {
		t.Fatalf("not wired: %v", m)
	}
	cm := m["customModelsConfig"].(map[string]any)["customModels"].(map[string]any)
	want := map[string]any{"apiProvider": "API_PROVIDER_GOOGLE_GEMINI", "modelName": "deepseek/pro"}
	if got, _ := json.Marshal(cm["magpie/deepseek/pro"]); string(got) != agyJSON(want) {
		t.Fatalf("custom model: %s", got)
	}
	if cm["magpie/deepseek/flash"] == nil || cm["mine"] == nil {
		t.Fatalf("custom models: %v", cm)
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift: %+v", d)
	}
	l := a.Launch()
	if want := "GEMINI_API_KEY=" + gateway.TokenFor("agy") + " GOOGLE_GEMINI_BASE_URL=" + gateway.URL() + " agy --settings '" + over + "' --model 'magpie/deepseek/pro'"; runtime.GOOS != "windows" && l != want {
		t.Fatalf("launch: %q", l)
	}
	if n := a.Notice(); !strings.Contains(n, l) {
		t.Fatalf("notice: %q", n)
	}
	if got := a.Field("model").Get(); got != "magpie/deepseek/pro" {
		t.Fatalf("model: %q", got)
	}
	// another of magpie's
	if err := a.Apply("model", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	// something else sets modelProvider back in magpie's file: agy asks
	// Google directly
	os.WriteFile(over, []byte(strings.Replace(agyRead(t, over), `"gemini"`, `"vertex"`, 1)), 0o644)
	if d := a.Drift(); d == nil || d.Kind != "unwired" {
		t.Fatalf("drift: %+v", d)
	}
	// the options: the user's own, then magpie's
	opts := a.Field("model").Options(a.Values())
	if len(opts) == 0 || opts[0].Value != "mine" || opts[0].Group != "Antigravity CLI" {
		t.Fatalf("options: %+v", opts)
	}
	// as installed: magpie's file gone, settings.json as it was
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(over); err == nil {
		t.Fatal("magpie's settings file left")
	}
	if agyJSON(read()) != agyJSON(was) {
		t.Fatalf("not restored:\n%s", agyRead(t, path))
	}
	if a.Launch() != "" {
		t.Fatal("launch after reset")
	}

	// the user picks one of their own while on magpie's: it is theirs in
	// settings.json, and magpie's file is gone
	a.Apply("model", "magpie/deepseek/pro")
	if err := a.Apply("model", "mine"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(over); err == nil || a.Launch() != "" {
		t.Fatal("still on magpie after picking their own")
	}

	// wired by an older magpie (modelProvider and model in settings.json,
	// the user's stashed): the next start moves it out of there
	os.WriteFile(path, []byte(orig), 0o644)
	stash(map[string]string{"agy:" + path + ":model": "mine", "agy:" + path + ":modelProvider": "vertex"})
	old := map[string]any{}
	json.Unmarshal([]byte(orig), &old)
	old["model"], old["modelProvider"] = "magpie/deepseek/pro", "gemini"
	oc := old["customModelsConfig"].(map[string]any)["customModels"].(map[string]any)
	oc["magpie/deepseek/pro"] = map[string]any{"apiProvider": "API_PROVIDER_GOOGLE_GEMINI", "modelName": "deepseek/pro"}
	b, _ := json.Marshal(old)
	os.WriteFile(path, b, 0o644)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if agyJSON(read()) != agyJSON(was) {
		t.Fatalf("settings.json not given back:\n%s", agyRead(t, path))
	}
	if !strings.Contains(a.Launch(), "--model 'magpie/deepseek/pro'") || a.Drift() != nil {
		t.Fatalf("after the move: %q %+v", a.Launch(), a.Drift())
	}
	a.Apply("model", "")

	// with nothing of the user's, magpie leaves nothing in settings.json
	os.WriteFile(path, []byte("{\n  \"trustedWorkspaces\": []\n}\n"), 0o644)
	a.Apply("model", "magpie/deepseek/pro")
	a.Apply("model", "")
	if got := strings.TrimSpace(agyRead(t, path)); got != "{\n  \"trustedWorkspaces\": []\n}" {
		t.Fatalf("left behind:\n%s", got)
	}
}

func agyJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func agyRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
