package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

func stepcodeWriteTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func stepcodeReadTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestStepCodeRegistered(t *testing.T) {
	byID, err := Find("stepcode")
	if err != nil || byID == nil {
		t.Fatalf("Find(\"stepcode\") failed: %v", err)
	}
	if byID.ID != "stepcode" {
		t.Fatalf("Find(\"stepcode\").ID = %q, want \"stepcode\"", byID.ID)
	}

	byBin, err := Find("step")
	if err != nil || byBin == nil {
		t.Fatalf("Find(\"step\") failed: %v", err)
	}
	if byBin.ID != "stepcode" {
		t.Fatalf("Find(\"step\").ID = %q, want \"stepcode\"", byBin.ID)
	}
	// StepCode 0.1.3's requests say "step (darwin 25.2.0; arm64)", read off
	// a real turn through the gateway; they count as StepCode's
	for _, ua := range []string{"step (darwin 25.2.0; arm64)", "step (linux 6.8.0; x64)"} {
		if got := usage.AgentOf(ua); got != "stepcode" {
			t.Fatalf("usage.AgentOf(%q) = %q, want stepcode", ua, got)
		}
	}
}

func TestStepCodeLifecycle(t *testing.T) {
	home, _ := museHome(t)
	dir := filepath.Join(home, ".stepcode")
	configPath := filepath.Join(dir, "config.toml")
	modelsPath := filepath.Join(dir, "models.json")
	authPath := filepath.Join(dir, "auth.json")

	origTOML := `# StepCode user settings
theme = "dark"
defaultProvider = "anthropic"
defaultModel = "claude-3-7-sonnet"
defaultThinkingLevel = "high"
enabledModels = ["anthropic/*", "custom/fast"]

[mcp_servers.test]
command = "node"
`
	origModels := `{
  "providers": {
    "anthropic": {
      "name": "Anthropic",
      "models": [
        {"id": "claude-3-7-sonnet"}
      ]
    }
  }
}
`
	origAuth := `{"apiKey":"secret-123"}`

	stepcodeWriteTestFile(t, configPath, origTOML)
	stepcodeWriteTestFile(t, modelsPath, origModels)
	stepcodeWriteTestFile(t, authPath, origAuth)

	a := stepcode(home)
	if a.Wired() {
		t.Fatalf("initially expected unwired")
	}

	if err := a.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !a.Wired() {
		t.Fatalf("expected wired after Connect")
	}

	cfgStr := stepcodeReadTestFile(t, configPath)
	if !strings.Contains(cfgStr, "# StepCode user settings") {
		t.Errorf("leading comment lost in config.toml:\n%s", cfgStr)
	}
	if !strings.Contains(cfgStr, `[mcp_servers.test]`) || !strings.Contains(cfgStr, `theme = "dark"`) {
		t.Errorf("unrelated settings lost in config.toml:\n%s", cfgStr)
	}
	if !strings.Contains(cfgStr, `defaultThinkingLevel = "high"`) {
		t.Errorf("defaultThinkingLevel lost during connect:\n%s", cfgStr)
	}

	var parsedCfg map[string]any
	if err := toml.Unmarshal([]byte(cfgStr), &parsedCfg); err != nil {
		t.Fatalf("config.toml invalid TOML: %v", err)
	}
	if parsedCfg["defaultProvider"] != "magpie" {
		t.Errorf("defaultProvider = %v, want magpie", parsedCfg["defaultProvider"])
	}

	if stepcodeReadTestFile(t, authPath) != origAuth {
		t.Errorf("auth.json modified during connect")
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err == nil {
		t.Errorf("unexpected settings.json created during connect")
	}

	if err := a.Apply("model", "magpie/deepseek/flash"); err != nil {
		t.Fatalf("Apply(model flash): %v", err)
	}

	if err := a.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if a.Wired() {
		t.Fatalf("still wired after Disconnect")
	}

	restoredCfgStr := stepcodeReadTestFile(t, configPath)
	var restoredCfg map[string]any
	if err := toml.Unmarshal([]byte(restoredCfgStr), &restoredCfg); err != nil {
		t.Fatalf("restored config.toml invalid: %v", err)
	}
	if restoredCfg["defaultProvider"] != "anthropic" || restoredCfg["defaultModel"] != "claude-3-7-sonnet" {
		t.Errorf("defaults not restored: %v / %v", restoredCfg["defaultProvider"], restoredCfg["defaultModel"])
	}
	if restoredCfg["defaultThinkingLevel"] != "high" {
		t.Errorf("defaultThinkingLevel not preserved after disconnect: %v", restoredCfg["defaultThinkingLevel"])
	}

	if stepcodeReadTestFile(t, authPath) != origAuth {
		t.Errorf("auth.json modified after disconnect")
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err == nil {
		t.Errorf("unexpected settings.json created after disconnect")
	}

	var restoredModels map[string]any
	if err := json.Unmarshal([]byte(stepcodeReadTestFile(t, modelsPath)), &restoredModels); err != nil {
		t.Fatalf("restored models.json invalid: %v", err)
	}
	provs := restoredModels["providers"].(map[string]any)
	if provs["anthropic"] == nil {
		t.Errorf("native provider anthropic lost from models.json")
	}
	if provs["magpie"] != nil {
		t.Errorf("magpie provider not removed on disconnect")
	}

	if err := a.Connect(); err != nil {
		t.Fatalf("reconnect Connect: %v", err)
	}
	if !a.Wired() {
		t.Fatalf("expected wired after reconnect")
	}
	if err := a.Disconnect(); err != nil {
		t.Fatalf("disconnect second time: %v", err)
	}
}

func TestStepCodeScopeRetention(t *testing.T) {
	home, _ := museHome(t)
	dir := filepath.Join(home, ".stepcode")
	configPath := filepath.Join(dir, "config.toml")
	modelsPath := filepath.Join(dir, "models.json")

	initialTOML := `defaultProvider = "native"
defaultModel = "one"
enabledModels = ["native/one"]
`
	stepcodeWriteTestFile(t, configPath, initialTOML)
	stepcodeWriteTestFile(t, modelsPath, `{"providers":{"native":{"name":"Native","models":[{"id":"one"}]}}}`)

	a := stepcode(home)

	// 1. native/one -> apply magpie/pro
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatalf("Apply pro: %v", err)
	}

	cfg, err := readStepcodeTOML(configPath)
	if err != nil || cfg.Scope == nil {
		t.Fatalf("failed reading scope after pro: %v", err)
	}
	if !slices.Contains(*cfg.Scope, "magpie/deepseek/pro") {
		t.Errorf("scope missing magpie/deepseek/pro: %v", *cfg.Scope)
	}

	// 2. user adds native/two
	userScope := append(*cfg.Scope, "native/two")
	raw, err := stepcodeScopeRaw(userScope)
	if err != nil {
		t.Fatal(err)
	}
	if err := edit.SetTOMLTopPreserving(configPath, edit.KV{Path: "enabledModels", Value: raw}); err != nil {
		t.Fatal(err)
	}

	// 3. apply flash
	if err := a.Apply("model", "magpie/deepseek/flash"); err != nil {
		t.Fatalf("Apply flash: %v", err)
	}

	// 4. user adds native/three
	cfg2, err := readStepcodeTOML(configPath)
	if err != nil || cfg2.Scope == nil {
		t.Fatal(err)
	}
	userScope2 := append(*cfg2.Scope, "native/three")
	raw2, err := stepcodeScopeRaw(userScope2)
	if err != nil {
		t.Fatal(err)
	}
	if err := edit.SetTOMLTopPreserving(configPath, edit.KV{Path: "enabledModels", Value: raw2}); err != nil {
		t.Fatal(err)
	}

	// 5. Disconnect
	if err := a.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	cfgRestored, err := readStepcodeTOML(configPath)
	if err != nil || cfgRestored.Scope == nil {
		t.Fatalf("failed reading restored scope: %v", err)
	}
	expected := []string{"native/one", "native/two", "native/three"}
	if !slices.Equal(*cfgRestored.Scope, expected) {
		t.Errorf("restored scope = %v, want %v", *cfgRestored.Scope, expected)
	}

	// User deleting scope entirely
	stepcodeWriteTestFile(t, configPath, `defaultProvider = "native"
defaultModel = "one"
enabledModels = ["native/one"]
`)
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := edit.DelTOMLTop(configPath, "enabledModels"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	cfgNoScope, err := readStepcodeTOML(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfgNoScope.Scope != nil {
		t.Errorf("expected deleted scope to remain deleted on disconnect, got: %v", *cfgNoScope.Scope)
	}
}

func TestStepCodeScopeWildcardMatch(t *testing.T) {
	home, _ := museHome(t)
	dir := filepath.Join(home, ".stepcode")
	configPath := filepath.Join(dir, "config.toml")
	modelsPath := filepath.Join(dir, "models.json")

	initialTOML := `defaultProvider = "native"
defaultModel = "one"
enabledModels = ["magpie/*/*"]
`
	stepcodeWriteTestFile(t, configPath, initialTOML)
	stepcodeWriteTestFile(t, modelsPath, `{"providers":{"native":{"name":"Native","models":[{"id":"one"}]}}}`)

	a := stepcode(home)
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}

	cfg, err := readStepcodeTOML(configPath)
	if err != nil || cfg.Scope == nil {
		t.Fatal(err)
	}
	if len(*cfg.Scope) != 1 || (*cfg.Scope)[0] != "magpie/*/*" {
		t.Errorf("wildcard scope modified: %v", *cfg.Scope)
	}
}

func TestStepCodeNativeChangedAndPreexisting(t *testing.T) {
	home, _ := museHome(t)
	dir := filepath.Join(home, ".stepcode")
	configPath := filepath.Join(dir, "config.toml")
	modelsPath := filepath.Join(dir, "models.json")

	stepcodeWriteTestFile(t, configPath, "defaultProvider = \"anthropic\"\ndefaultModel = \"claude-3-5\"\ncustom_opt = \"keep-me\"\n")
	stepcodeWriteTestFile(t, modelsPath, "{\n  \"providers\": {\n    \"magpie\": {\n      \"name\": \"my-preexisting-magpie\",\n      \"baseUrl\": \"https://custom.endpoint/v1\",\n      \"customKey\": \"keep-me-too\"\n    }\n  }\n}\n")

	a := stepcode(home)
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}

	stepcodeWriteTestFile(t, configPath, "defaultProvider = \"user-openai\"\ndefaultModel = \"gpt-4o\"\ncustom_opt = \"keep-me\"\n")

	if !a.Beside() {
		t.Fatalf("expected Beside() == true after user changed native provider")
	}

	if err := a.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	cfg, err := readStepcodeTOML(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if stepcodeValue(cfg.Provider) != "user-openai" || stepcodeValue(cfg.Model) != "gpt-4o" {
		t.Errorf("user native model was not retained: %v / %v", cfg.Provider, cfg.Model)
	}

	modelsContent := stepcodeReadTestFile(t, modelsPath)
	var parsedModels map[string]any
	if err := json.Unmarshal([]byte(modelsContent), &parsedModels); err != nil {
		t.Fatal(err)
	}
	provs := parsedModels["providers"].(map[string]any)
	magpieProv := provs["magpie"].(map[string]any)
	if magpieProv["name"] != "my-preexisting-magpie" || magpieProv["customKey"] != "keep-me-too" {
		t.Errorf("preexisting providers.magpie was not restored: %v", magpieProv)
	}

	if err := a.Unwire(); err != nil {
		t.Errorf("repeat Unwire failed: %v", err)
	}

	if err := a.Sync(); err != nil {
		t.Errorf("Sync after disconnect failed: %v", err)
	}
	modelsContent2 := stepcodeReadTestFile(t, modelsPath)
	if modelsContent != modelsContent2 {
		t.Errorf("Sync re-wired after disconnect")
	}

	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Setenv("USERPROFILE", emptyHome)
	aEmpty := stepcode(emptyHome)
	if err := aEmpty.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	emptyConfig := filepath.Join(emptyHome, ".stepcode", "config.toml")
	emptyModels := filepath.Join(emptyHome, ".stepcode", "models.json")
	if _, err := os.Stat(emptyConfig); err != nil {
		t.Fatalf("expected config.toml created")
	}
	if _, err := os.Stat(emptyModels); err != nil {
		t.Fatalf("expected models.json created")
	}
	if err := aEmpty.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(emptyConfig); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected empty config.toml removed on disconnect")
	}
	if _, err := os.Stat(emptyModels); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected empty models.json removed on disconnect")
	}
}

func TestStepCodeJSONCCommentsAndTrailingComma(t *testing.T) {
	home, _ := museHome(t)
	dir := filepath.Join(home, ".stepcode")
	configPath := filepath.Join(dir, "config.toml")
	modelsPath := filepath.Join(dir, "models.json")

	jsoncModels := `{
  // User comments in models.json
  "providers": {
    "my-openai": {
      "name": "My OpenAI",
      "models": [
        {"id": "gpt-4o",}, // trailing comma
      ],
    },
  },
}
`
	stepcodeWriteTestFile(t, configPath, "defaultProvider = \"my-openai\"\ndefaultModel = \"gpt-4o\"\n")
	stepcodeWriteTestFile(t, modelsPath, jsoncModels)

	a := stepcode(home)
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatalf("Apply on JSONC models.json failed: %v", err)
	}

	content := stepcodeReadTestFile(t, modelsPath)
	if !strings.Contains(content, "// User comments in models.json") {
		t.Errorf("JSONC comments lost in models.json:\n%s", content)
	}

	if err := a.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	contentAfter := stepcodeReadTestFile(t, modelsPath)
	if !strings.Contains(contentAfter, "// User comments in models.json") {
		t.Errorf("JSONC comments lost after disconnect:\n%s", contentAfter)
	}
}

func TestStepCodeSyncAndDrift(t *testing.T) {
	home, _ := museHome(t)
	a := stepcode(home)
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}

	modelsPath := filepath.Join(home, ".stepcode", "models.json")

	modelsContent := stepcodeReadTestFile(t, modelsPath)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(modelsContent), &parsed); err != nil {
		t.Fatal(err)
	}
	magpieProv := parsed["providers"].(map[string]any)["magpie"].(map[string]any)
	magpieProv["customUnknownKey"] = "keep-this"
	b, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	stepcodeWriteTestFile(t, modelsPath, string(b))

	if err := a.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	modelsContent = stepcodeReadTestFile(t, modelsPath)
	if err := json.Unmarshal([]byte(modelsContent), &parsed); err != nil {
		t.Fatal(err)
	}
	magpieProv = parsed["providers"].(map[string]any)["magpie"].(map[string]any)
	if magpieProv["customUnknownKey"] != "keep-this" {
		t.Errorf("Sync dropped unknown key in providers.magpie: %v", magpieProv)
	}

	fixedTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(modelsPath, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	bytesBefore := stepcodeReadTestFile(t, modelsPath)

	if err := a.Sync(); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	info, err := os.Stat(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(fixedTime) {
		t.Errorf("idempotent Sync touched mtime: got %v, want %v", info.ModTime(), fixedTime)
	}
	if stepcodeReadTestFile(t, modelsPath) != bytesBefore {
		t.Errorf("idempotent Sync changed file bytes")
	}

	magpieProv["baseUrl"] = "http://127.0.0.1:9999/v1"
	b, _ = json.MarshalIndent(parsed, "", "  ")
	stepcodeWriteTestFile(t, modelsPath, string(b))
	checkMsg := a.Check()
	if !strings.Contains(checkMsg, "baseUrl") {
		t.Errorf("Check() did not report URL drift: %q", checkMsg)
	}

	magpieProv["baseUrl"] = here(home).v1()
	magpieProv["apiKey"] = "drifted-key"
	b, _ = json.MarshalIndent(parsed, "", "  ")
	stepcodeWriteTestFile(t, modelsPath, string(b))
	checkKeyMsg := a.Check()
	if !strings.Contains(checkKeyMsg, "apiKey") && !strings.Contains(checkKeyMsg, "changed") {
		t.Errorf("Check() did not report apiKey drift: %q", checkKeyMsg)
	}

	if err := a.Reapply(); err != nil {
		t.Fatalf("Reapply: %v", err)
	}
	if a.Check() != "" {
		t.Errorf("Check() still reports drift after Reapply: %q", a.Check())
	}
}

func TestStepCodeCatalogUpdateSync(t *testing.T) {
	home, _ := museHome(t)
	a := stepcode(home)
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}

	modelsPath := filepath.Join(home, ".stepcode", "models.json")
	if !strings.Contains(stepcodeReadTestFile(t, modelsPath), "deepseek/pro") {
		t.Fatalf("models.json missing initial model")
	}

	if err := provider.Save(provider.Provider{
		ID:     "deepseek",
		Name:   "DeepSeek",
		Chat:   "https://api.deepseek.com/v1",
		Key:    "k",
		Models: []string{"pro-v2"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := a.Sync(); err != nil {
		t.Fatalf("Sync with updated catalog: %v", err)
	}

	updated := stepcodeReadTestFile(t, modelsPath)
	if !strings.Contains(updated, "deepseek/pro-v2") {
		t.Errorf("Sync did not add new catalog model deepseek/pro-v2:\n%s", updated)
	}
	if strings.Contains(updated, "\"id\": \"deepseek/pro\"") {
		t.Errorf("Sync did not prune removed catalog model deepseek/pro:\n%s", updated)
	}
}

func TestStepCodeMetadataAndLANKey(t *testing.T) {
	home, _ := museHome(t)
	if err := provider.Save(provider.Provider{
		ID:        "resp-prov",
		Name:      "RespProv",
		Key:       "k",
		Responses: "https://resp.example.com/v1",
		Models:    []string{"resp-mod"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("resp-prov", "https://resp.example.com/v1", []catalog.Model{
		{
			ID:      "resp-mod",
			Name:    "Resp Mod",
			Context: 128000,
			Output:  4096,
			APIs:    []string{string(provider.Responses)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	a := stepcode(home)
	if err := a.Apply("model", "magpie/resp-prov/resp-mod"); err != nil {
		t.Fatal(err)
	}

	modelsPath := filepath.Join(home, ".stepcode", "models.json")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stepcodeReadTestFile(t, modelsPath)), &parsed); err != nil {
		t.Fatal(err)
	}
	magpieProv := parsed["providers"].(map[string]any)["magpie"].(map[string]any)
	if magpieProv["api"] != "openai-completions" {
		t.Errorf("api = %v, want openai-completions", magpieProv["api"])
	}
	modelsList := magpieProv["models"].([]any)
	if len(modelsList) == 0 {
		t.Fatalf("empty models list")
	}
	var targetModel map[string]any
	for _, m := range modelsList {
		mm := m.(map[string]any)
		if mm["id"] == "resp-prov/resp-mod" {
			targetModel = mm
			break
		}
	}
	if targetModel == nil {
		t.Fatalf("resp-prov/resp-mod not found in models: %v", modelsList)
	}
	if targetModel["contextWindow"] != float64(128000) {
		t.Errorf("contextWindow = %v, want 128000", targetModel["contextWindow"])
	}
	if targetModel["maxTokens"] != float64(4096) {
		t.Errorf("maxTokens = %v, want 4096", targetModel["maxTokens"])
	}
	if targetModel["api"] != "openai-responses" {
		t.Errorf("native protocol api = %v, want openai-responses", targetModel["api"])
	}

	// WSL NAT key runtime test: actually Apply in separate WSL home
	wslAddr := "http://172.23.80.1:3425"
	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	lanKey := access.LANSecret()

	wslHome := t.TempDir()
	wslPl := place{home: wslHome, spell: func(s string) string { return s }, base: func() string { return wslAddr }}
	wslAgent := stepcodeIn(wslPl)
	if err := wslAgent.Apply("model", "magpie/resp-prov/resp-mod"); err != nil {
		t.Fatalf("wslAgent.Apply: %v", err)
	}

	wslModelsPath := filepath.Join(wslHome, ".stepcode", "models.json")
	var wslParsed map[string]any
	if err := json.Unmarshal([]byte(stepcodeReadTestFile(t, wslModelsPath)), &wslParsed); err != nil {
		t.Fatal(err)
	}
	wslMagpieProv := wslParsed["providers"].(map[string]any)["magpie"].(map[string]any)
	if wslMagpieProv["baseUrl"] != wslAddr+"/v1" {
		t.Errorf("wsl baseUrl = %v, want %v", wslMagpieProv["baseUrl"], wslAddr+"/v1")
	}
	if wslMagpieProv["apiKey"] != lanKey {
		t.Errorf("wsl apiKey = %v, want lanKey %v", wslMagpieProv["apiKey"], lanKey)
	}
	if err := wslAgent.Disconnect(); err != nil {
		t.Fatalf("wslAgent.Disconnect: %v", err)
	}
}

func TestStepCodeStrictValidationAndRollback(t *testing.T) {
	home, _ := museHome(t)
	dir := filepath.Join(home, ".stepcode")
	configPath := filepath.Join(dir, "config.toml")
	modelsPath := filepath.Join(dir, "models.json")

	stepcodeWriteTestFile(t, configPath, "bad toml [[ [")
	a := stepcode(home)
	if err := a.Apply("model", "magpie/deepseek/pro"); err == nil {
		t.Errorf("expected error applying on malformed TOML")
	}
	if a.Check() == "" {
		t.Errorf("Check() should report malformed config as wiring error")
	}

	stepcodeWriteTestFile(t, configPath, "defaultProvider = 12345\n")
	if err := a.Apply("model", "magpie/deepseek/pro"); err == nil {
		t.Errorf("expected error on non-string defaultProvider")
	}

	stepcodeWriteTestFile(t, configPath, "enabledModels = \"not-a-list\"\n")
	if err := a.Apply("model", "magpie/deepseek/pro"); err == nil {
		t.Errorf("expected error on non-array enabledModels")
	}

	stepcodeWriteTestFile(t, configPath, "defaultProvider = \"anthropic\"\ndefaultModel = \"claude\"\n")
	stepcodeWriteTestFile(t, modelsPath, "not valid json {")
	if err := a.Apply("model", "magpie/deepseek/pro"); err == nil {
		t.Errorf("expected error applying on malformed models.json")
	}

	stepcodeWriteTestFile(t, modelsPath, `{"providers": 12345}`)
	if err := a.Apply("model", "magpie/deepseek/pro"); err == nil {
		t.Errorf("expected error applying on non-object providers in models.json")
	}
	stepcodeWriteTestFile(t, modelsPath, `{"providers": {"magpie": "not-an-object"}}`)
	if err := a.Apply("model", "magpie/deepseek/pro"); err == nil {
		t.Errorf("expected error applying on non-object providers.magpie in models.json")
	}

	stepcodeWriteTestFile(t, configPath, "defaultProvider = \"\"\ndefaultModel = \"\"\n")
	stepcodeWriteTestFile(t, modelsPath, `{"providers": {}}`)
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	cfg, err := readStepcodeTOML(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider == nil || *cfg.Provider != "" || cfg.Model == nil || *cfg.Model != "" {
		t.Errorf("empty defaults not restored as empty strings: %v / %v", cfg.Provider, cfg.Model)
	}
}

func TestStepCodeStashSafety(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"null", "null"},
		{"array", "[]"},
		{"null_entry", `{"stepcode.wiring":"null"}`},
		{"invalid_entry", `{"stepcode.wiring":"invalid"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := museHome(t)
			dir := filepath.Join(home, ".stepcode")
			configPath := filepath.Join(dir, "config.toml")
			modelsPath := filepath.Join(dir, "models.json")
			initialTOML := "defaultProvider = \"native\"\ndefaultModel = \"test\"\n"
			initialModels := `{"providers":{"native":{"name":"Native"}}}`

			stepcodeWriteTestFile(t, configPath, initialTOML)
			stepcodeWriteTestFile(t, modelsPath, initialModels)
			stepcodeWriteTestFile(t, stashPath(), tc.raw)

			a := stepcode(home)
			err := a.Field("model").Set("magpie/deepseek/pro")
			if err == nil {
				t.Fatalf("expected error with corrupted stash %s", tc.raw)
			}

			if got := stepcodeReadTestFile(t, configPath); got != initialTOML {
				t.Errorf("config.toml modified on stash error: %s", got)
			}
			if got := stepcodeReadTestFile(t, modelsPath); got != initialModels {
				t.Errorf("models.json modified on stash error: %s", got)
			}
			if got := stepcodeReadTestFile(t, stashPath()); got != tc.raw {
				t.Errorf("stash.json modified on stash error: %s", got)
			}
		})
	}

	t.Run("permission_and_unwire", func(t *testing.T) {
		home, _ := museHome(t)
		dir := filepath.Join(home, ".stepcode")
		configPath := filepath.Join(dir, "config.toml")
		modelsPath := filepath.Join(dir, "models.json")
		stepcodeWriteTestFile(t, configPath, "defaultProvider = \"native\"\ndefaultModel = \"test\"\n")
		stepcodeWriteTestFile(t, modelsPath, `{"providers":{"native":{"name":"Native"}}}`)

		_ = os.Remove(stashPath())

		a := stepcode(home)
		if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
			t.Fatalf("Field.Set failed: %v", err)
		}

		if runtime.GOOS != "windows" {
			info, err := os.Stat(stashPath())
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o077 != 0 {
				t.Errorf("stash permissions not 0600: %v", info.Mode().Perm())
			}
		}

		if err := a.Unwire(); err != nil {
			t.Fatalf("Unwire failed: %v", err)
		}
	})

	t.Run("directory_failure", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("skipping directory permission failure test on windows or as root")
		}
		home, _ := museHome(t)
		dir := filepath.Join(home, ".stepcode")
		configPath := filepath.Join(dir, "config.toml")
		modelsPath := filepath.Join(dir, "models.json")
		initialTOML := "defaultProvider = \"native\"\ndefaultModel = \"test\"\n"
		stepcodeWriteTestFile(t, configPath, initialTOML)
		stepcodeWriteTestFile(t, modelsPath, `{"providers":{"native":{"name":"Native"}}}`)

		stashDir := filepath.Dir(stashPath())
		if err := os.MkdirAll(stashDir, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(stashPath())
		if err := os.Chmod(stashDir, 0o500); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chmod(stashDir, 0o755) }()

		a := stepcode(home)
		err := a.Field("model").Set("magpie/deepseek/pro")
		if err == nil {
			t.Fatalf("expected error with unwritable stash directory")
		}

		_ = os.Chmod(stashDir, 0o755)
		if got := stepcodeReadTestFile(t, configPath); got != initialTOML {
			t.Errorf("config.toml modified on stash dir failure: %s", got)
		}
	})
}

func TestStepCodeAtomicRollbackOnSecondFileFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("skipping symlink directory permission failure test on windows or as root")
	}

	home, _ := museHome(t)
	dir := filepath.Join(home, ".stepcode")
	configPath := filepath.Join(dir, "config.toml")
	modelsPath := filepath.Join(dir, "models.json")

	initialModels := `{"providers": {"native": {"name": "Native"}}}`
	initialConfig := "defaultProvider = \"native\"\ndefaultModel = \"test\"\n"
	initialStash := "{}"
	stepcodeWriteTestFile(t, modelsPath, initialModels)
	stepcodeWriteTestFile(t, stashPath(), initialStash)

	// Create separate read-only folder holding target config file
	targetDir := filepath.Join(home, "target-readonly")
	targetConfig := filepath.Join(targetDir, "real-config.toml")
	stepcodeWriteTestFile(t, targetConfig, initialConfig)

	// Symlink config.toml to targetConfig
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetConfig, configPath); err != nil {
		t.Fatal(err)
	}

	// Make target directory read-only so atomic temporary write for config.toml fails
	if err := os.Chmod(targetConfig, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(targetDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chmod(targetDir, 0o755)
		_ = os.Chmod(targetConfig, 0o644)
	}()

	a := stepcode(home)
	err := a.Apply("model", "magpie/deepseek/pro")
	if err == nil {
		t.Fatalf("expected error writing to read-only symlink target")
	}
	if !strings.Contains(err.Error(), targetDir) {
		t.Errorf("expected error to contain targetDir %q, got: %v", targetDir, err)
	}

	_ = os.Chmod(targetDir, 0o755)
	_ = os.Chmod(targetConfig, 0o644)

	currentModels := stepcodeReadTestFile(t, modelsPath)
	if currentModels != initialModels {
		t.Errorf("atomic rollback failed: models.json was not restored:\n%s", currentModels)
	}
	currentConfig := stepcodeReadTestFile(t, targetConfig)
	if currentConfig != initialConfig {
		t.Errorf("atomic rollback failed: config.toml was not restored:\n%s", currentConfig)
	}
	currentStash := stepcodeReadTestFile(t, stashPath())
	if currentStash != initialStash {
		t.Errorf("atomic rollback failed: stash.json was not restored:\n%s", currentStash)
	}
	state, loadErr := loadStepcodeState(here(home))
	if loadErr != nil {
		t.Fatalf("loadStepcodeState error: %v", loadErr)
	}
	if state != nil {
		t.Errorf("atomic rollback failed: stash record was left behind")
	}
}

func TestStepCodeDirectoryAndWSL(t *testing.T) {
	home := t.TempDir()

	d := stepcodeDir(here(home))
	if d != filepath.Join(home, ".stepcode") {
		t.Errorf("default dir = %q, want %q", d, filepath.Join(home, ".stepcode"))
	}

	absTarget := filepath.Join(home, "custom", "agent")
	t.Setenv("STEP_CODING_AGENT_DIR", absTarget)
	d = stepcodeDir(here(home))
	if d != filepath.Join(home, "custom") {
		t.Errorf("abs STEP_CODING_AGENT_DIR dir = %q, want %q", d, filepath.Join(home, "custom"))
	}

	t.Setenv("STEP_CODING_AGENT_DIR", "rel/agent")
	d = stepcodeDir(here(home))
	if d != filepath.Join(home, ".stepcode") {
		t.Errorf("rel STEP_CODING_AGENT_DIR dir = %q, want default %q", d, filepath.Join(home, ".stepcode"))
	}
	t.Setenv("STEP_CODING_AGENT_DIR", "")

	t.Setenv("STEPCODE_CONFIG_DIR", ".my-step")
	d = stepcodeDir(here(home))
	if d != filepath.Join(home, ".my-step") {
		t.Errorf("STEPCODE_CONFIG_DIR dir = %q, want %q", d, filepath.Join(home, ".my-step"))
	}
	t.Setenv("STEPCODE_CONFIG_DIR", "")

	t.Setenv("STEP_CODING_AGENT_DIR", absTarget)
	t.Setenv("STEPCODE_CONFIG_DIR", ".my-step")
	wslPlace := place{home: home, spell: func(s string) string { return s }}
	d = stepcodeDir(wslPlace)
	if d != filepath.Join(home, ".stepcode") {
		t.Errorf("WSL dir = %q, want .stepcode %q", d, filepath.Join(home, ".stepcode"))
	}
	t.Setenv("STEP_CODING_AGENT_DIR", "")
	t.Setenv("STEPCODE_CONFIG_DIR", "")

	stepDir := filepath.Join(home, "bin")
	dummyStep := filepath.Join(stepDir, "step")
	stepcodeWriteTestFile(t, dummyStep, "#!/bin/sh\nexit 0\n")
	_ = os.Chmod(dummyStep, 0o755)
	t.Setenv("PATH", stepDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	a := stepcode(home)
	if a.Detected() {
		t.Errorf("Detected() = true with only bare step CLI on PATH")
	}

	stepcodeWriteTestFile(t, filepath.Join(home, ".stepcode", "config.toml"), "theme = \"dark\"\n")
	if !a.Detected() {
		t.Errorf("Detected() = false after .stepcode directory created")
	}
}

func TestStepCodeRealCLISandbox(t *testing.T) {
	stepBin := os.Getenv("MAGPIE_TEST_STEPCODE_BIN")
	if stepBin == "" {
		t.Skip("skipping real CLI sandbox test: MAGPIE_TEST_STEPCODE_BIN not set")
	}
	if !filepath.IsAbs(stepBin) {
		t.Fatalf("MAGPIE_TEST_STEPCODE_BIN must be an absolute path, got %q", stepBin)
	}
	if _, err := os.Stat(stepBin); err != nil {
		t.Fatalf("MAGPIE_TEST_STEPCODE_BIN binary not found: %v", err)
	}

	home, _ := museHome(t)
	agentDir := filepath.Join(home, ".stepcode", "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("STEP_CODING_AGENT_DIR", agentDir)

	stepcodeWriteTestFile(t, filepath.Join(home, ".stepcode", "config.toml"), "[telemetry]\nenabled = false\n")

	a := stepcode(home)
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmdPath := exec.CommandContext(ctx, stepBin, "config", "path", "--json")
	cmdPath.Dir = home
	cmdPath.Env = []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"STEP_CODING_AGENT_DIR=" + agentDir,
		"PATH=" + os.Getenv("PATH"),
	}
	outPath, err := cmdPath.CombinedOutput()
	if err != nil {
		t.Fatalf("step config path --json failed: %v, output:\n%s", err, string(outPath))
	}
	t.Logf("step config path output:\n%s", string(outPath))

	var pathResult struct {
		GlobalSettingsPath string `json:"globalSettingsPath"`
		GlobalModelsPath   string `json:"globalModelsPath"`
	}
	if err := json.Unmarshal(outPath, &pathResult); err != nil {
		t.Fatalf("failed parsing step config path JSON: %v", err)
	}
	expectedCfg := filepath.Join(home, ".stepcode", "config.toml")
	expectedModels := filepath.Join(home, ".stepcode", "models.json")
	if pathResult.GlobalSettingsPath != expectedCfg {
		t.Errorf("globalSettingsPath = %q, want %q", pathResult.GlobalSettingsPath, expectedCfg)
	}
	if pathResult.GlobalModelsPath != expectedModels {
		t.Errorf("globalModelsPath = %q, want %q", pathResult.GlobalModelsPath, expectedModels)
	}

	cmdList := exec.CommandContext(ctx, stepBin, "--list-models")
	cmdList.Dir = home
	cmdList.Env = cmdPath.Env
	outList, err := cmdList.CombinedOutput()
	if err != nil {
		t.Fatalf("step --list-models failed: %v, output:\n%s", err, string(outList))
	}
	listStr := string(outList)
	t.Logf("step --list-models output:\n%s", listStr)
	if !stepcodeListsMagpie(listStr) || !strings.Contains(listStr, "deepseek/pro") {
		t.Errorf("step --list-models did not list magpie/deepseek/pro:\n%s", listStr)
	}

	if err := a.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	cmdListAfter := exec.CommandContext(ctx, stepBin, "--list-models")
	cmdListAfter.Dir = home
	cmdListAfter.Env = cmdPath.Env
	outListAfter, err := cmdListAfter.CombinedOutput()
	if err != nil {
		t.Fatalf("step --list-models after disconnect failed: %v, output:\n%s", err, string(outListAfter))
	}
	// a row of the list is "<provider> <model> …"; the folder step prints
	// its docs from may itself have "magpie" in it, so only a row counts
	if stepcodeListsMagpie(string(outListAfter)) {
		t.Errorf("step --list-models still lists magpie after Disconnect:\n%s", string(outListAfter))
	}
}

// stepcodeListsMagpie is whether step --list-models has a row under the
// magpie provider.
func stepcodeListsMagpie(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == "magpie" {
			return true
		}
	}
	return false
}
