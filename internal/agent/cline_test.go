package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func TestCline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLINE_DIR", "")
	t.Setenv("CLINE_DATA_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".cline", "data", "settings", "providers.json")
	type entry struct {
		Settings map[string]any `json:"settings"`
	}
	type file struct {
		Version   int              `json:"version"`
		LastUsed  string           `json:"lastUsedProvider"`
		Providers map[string]entry `json:"providers"`
	}
	read := func() (file, string) {
		var f file
		b, _ := os.ReadFile(path)
		json.Unmarshal(b, &f)
		return f, string(b)
	}
	mpath := filepath.Join(filepath.Dir(path), "models.json")
	type list struct {
		Provider map[string]any            `json:"provider"`
		Models   map[string]map[string]any `json:"models"`
	}
	models := func() (map[string]list, string) {
		var f struct {
			Providers map[string]list `json:"providers"`
		}
		b, _ := os.ReadFile(mpath)
		json.Unmarshal(b, &f)
		return f.Providers, string(b)
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	mine := `{"provider":{"name":"OpenAI Compatible","baseUrl":"https://x/v1","defaultModelId":"mine"},"models":{"mine":{"id":"mine","name":"mine"}}}`
	os.WriteFile(mpath, []byte(`{"version":1,"providers":{"openai-compatible":`+mine+`,"ollama":{"models":{"q":{"id":"q"}}}}}`), 0o600)
	os.WriteFile(path, []byte(`{"version":1,"lastUsedProvider":"anthropic","modes":{},"providers":{
  "anthropic":{"settings":{"provider":"anthropic","apiKey":"sk-a","model":"claude-opus-5","reasoning":{"effort":"high"}},"updatedAt":"2026-09-01T00:00:00.000Z","tokenSource":"manual"},
  "openai-compatible":{"settings":{"provider":"openai-compatible","apiKey":"sk-o","model":"mine","baseUrl":"https://x/v1"},"updatedAt":"2026-09-01T00:00:00.000Z","tokenSource":"manual"}}}`), 0o600)
	// the VS Code extension's state, which it takes its provider from first
	data := filepath.Join(home, ".cline", "data")
	statePath, secretsPath := filepath.Join(data, "globalState.json"), filepath.Join(data, "secrets.json")
	userState := `{"actModeApiProvider":"anthropic","planModeApiProvider":"anthropic","actModeOpenAiModelId":"mine",` +
		`"actModeOpenAiModelInfo":{"maxTokens":10},"openAiBaseUrl":"https://x/v1","telemetrySetting":"disabled"}`
	os.WriteFile(statePath, []byte(userState), 0o600)
	os.WriteFile(secretsPath, []byte(`{"openAiApiKey":"sk-o","anthropicApiKey":"sk-a"}`), 0o600)
	vscode := func() (map[string]any, map[string]any) {
		var s, k map[string]any
		b, _ := os.ReadFile(statePath)
		json.Unmarshal(b, &s)
		b, _ = os.ReadFile(secretsPath)
		json.Unmarshal(b, &k)
		return s, k
	}

	a := cline(home)
	f, e := a.Field("model"), a.Field("effort")
	if f.Get() != "claude-opus-5" || e.Get() != "high" {
		t.Fatalf("get: %q %q", f.Get(), e.Get())
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	c, raw := read()
	s := c.Providers["openai-compatible"].Settings
	if c.LastUsed != "openai-compatible" || s["model"] != "deepseek/pro" || s["baseUrl"] != gatewayV1() || s["apiKey"] != gateway.Token ||
		s["headers"].(map[string]any)["User-Agent"] != "cline" || s["reasoning"].(map[string]any)["effort"] != "high" ||
		c.Providers["anthropic"].Settings["apiKey"] != "sk-a" {
		t.Fatalf("magpie:\n%s", raw)
	}
	// Cline's openai-compatible lists only gpt-4o: magpie's models take
	// its place in models.json, the user's other entries kept
	ml, mraw := models()
	if m := ml["openai-compatible"]; m.Provider["baseUrl"] != gatewayV1() || m.Provider["defaultModelId"] != "deepseek/pro" ||
		m.Models["deepseek/pro"] == nil || m.Models["deepseek/flash"] == nil || m.Models["mine"] != nil || ml["ollama"].Models["q"] == nil {
		t.Fatalf("models:\n%s", mraw)
	}
	if f.Get() != "magpie/deepseek/pro" || e.Get() != "high" || a.Check() != "" {
		t.Fatalf("get: %q %q %q", f.Get(), e.Get(), a.Check())
	}
	st, sec := vscode()
	if st["actModeApiProvider"] != "openai" || st["planModeApiProvider"] != "openai" || st["actModeOpenAiModelId"] != "deepseek/pro" ||
		st["planModeOpenAiModelId"] != "deepseek/pro" || st["openAiBaseUrl"] != gatewayV1() || st["actModeOpenAiModelInfo"] != nil ||
		st["telemetrySetting"] != "disabled" || sec["openAiApiKey"] != gateway.Token || sec["anthropicApiKey"] != "sk-a" {
		t.Fatalf("vscode: %v %v", st, sec)
	}
	// another magpie model keeps what was stashed first; the effort is set
	// on magpie's provider
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := e.Set("low"); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	if s := c.Providers["openai-compatible"].Settings; s["model"] != "deepseek/flash" || s["reasoning"].(map[string]any)["effort"] != "low" ||
		s["reasoning"].(map[string]any)["enabled"] != true {
		t.Fatalf("flash:\n%s", raw)
	}
	if st, _ := vscode(); st["actModeOpenAiModelId"] != "deepseek/flash" {
		t.Fatalf("vscode flash: %v", st)
	}
	// Cline's pickers turn thinking off as enabled false, which drops an
	// effort beside it: that reads as none, and an effort turns it back on
	edit.SetJSON(path, edit.KV{Path: "providers.openai-compatible.settings.reasoning", Value: map[string]any{"enabled": false, "effort": "low"}})
	if e.Get() != "none" {
		t.Fatalf("off: %q", e.Get())
	}
	if err := e.Set("high"); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	if r := c.Providers["openai-compatible"].Settings["reasoning"].(map[string]any); r["enabled"] != true || r["effort"] != "high" {
		t.Fatalf("on again:\n%s", raw)
	}
	if err := e.Set("none"); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	if r := c.Providers["openai-compatible"].Settings["reasoning"].(map[string]any); r["enabled"] != false || r["effort"] != nil || e.Get() != "none" {
		t.Fatalf("none:\n%s", raw)
	}
	e.Set("low")

	// back to Cline's own: the user's openai-compatible and provider return
	if err := f.Set("claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	if c.LastUsed != "anthropic" || c.Providers["anthropic"].Settings["model"] != "claude-sonnet-5" ||
		c.Providers["openai-compatible"].Settings["apiKey"] != "sk-o" || c.Providers["openai-compatible"].Settings["baseUrl"] != "https://x/v1" {
		t.Fatalf("own:\n%s", raw)
	}
	if ml, mraw := models(); ml["openai-compatible"].Models["mine"] == nil || ml["openai-compatible"].Models["deepseek/pro"] != nil ||
		ml["openai-compatible"].Provider["baseUrl"] != "https://x/v1" || ml["ollama"].Models["q"] == nil {
		t.Fatalf("own models:\n%s", mraw)
	}
	// the extension's state is the user's again, key and all
	var want map[string]any
	json.Unmarshal([]byte(userState), &want)
	if st, sec := vscode(); !reflect.DeepEqual(st, want) || sec["openAiApiKey"] != "sk-o" || sec["anthropicApiKey"] != "sk-a" {
		t.Fatalf("vscode own: %v %v", st, sec)
	}

	// reset from magpie, with no Cline settings before it: nothing of
	// magpie's is left
	os.Remove(path)
	os.Remove(mpath)
	if f.Get() != "" {
		t.Fatalf("empty get: %q", f.Get())
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	if c.Version != 1 || c.LastUsed != "openai-compatible" {
		t.Fatalf("new file:\n%s", raw)
	}
	if st, _ := os.Stat(path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 { // Windows has no such bits
		t.Fatalf("mode %v", st.Mode())
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	c, raw = read()
	if c.LastUsed != "" || len(c.Providers) != 0 || c.Version != 1 {
		t.Fatalf("reset:\n%s", raw)
	}
	if ml, mraw := models(); len(ml) != 0 || !strings.Contains(mraw, `"version"`) {
		t.Fatalf("reset models:\n%s", mraw)
	}
	if st, sec := vscode(); !reflect.DeepEqual(st, want) || sec["openAiApiKey"] != "sk-o" {
		t.Fatalf("vscode reset: %v %v", st, sec)
	}

	// $CLINE_DATA_DIR is where the files are, as for Cline
	t.Setenv("CLINE_DATA_DIR", filepath.Join(home, "d"))
	if p := cline(home).Path; p != filepath.Join(home, "d", "settings", "providers.json") {
		t.Fatalf("CLINE_DATA_DIR: %q", p)
	}
}
