package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// copilotJBUser is byok.json as the Copilot language server 1.545.1 (in the
// JetBrains plugin 1.18.0) wrote it for copilot/byok/saveCustomProviderConfig
// and saveModel of a custom endpoint provider of the user's: its bytes as
// they were, one line and a newline.
const copilotJBUser = `{"refprov-api-key":"refkey","refprov-provider-config":{"groupName":"refprov","apiType":"chatCompletions"},"refprov-models-config":{"r1":{"deploymentUrl":"http://127.0.0.1:3519/v1/chat/completions","isRegistered":true,"isCustomModel":true,"modelCapabilities":{"name":"Ref One","maxInputTokens":100000,"maxOutputTokens":8000,"toolCalling":true,"vision":false}}}}
`

// copilotJBModel is a model of a custom endpoint provider as the server
// keeps one; nothing else taken.
type copilotJBModel struct {
	DeploymentURL     string `json:"deploymentUrl"`
	IsRegistered      *bool  `json:"isRegistered"`
	IsCustomModel     *bool  `json:"isCustomModel"`
	ModelCapabilities struct {
		Name            string `json:"name"`
		MaxInputTokens  int    `json:"maxInputTokens"`
		MaxOutputTokens int    `json:"maxOutputTokens"`
		ToolCalling     bool   `json:"toolCalling"`
		Vision          *bool  `json:"vision"`
	} `json:"modelCapabilities"`
}

func strictJSON(t *testing.T, raw []byte, v any) {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		t.Fatalf("not the server's format: %v\n%s", err, raw)
	}
}

// Copilot in JetBrains IDEs takes magpie as a custom endpoint provider in
// the language server's byok.json, written as the server writes its own,
// beside the user's providers and keys, which stay; a model hidden in the
// IDE stays hidden through a sync; off takes out magpie's keys alone.
func TestCopilotJetBrains(t *testing.T) {
	home := syncHome(t)
	path := filepath.Join(home, ".config", "github-copilot", "byok.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(copilotJBUser), 0o600)

	if !slices.ContainsFunc(All(), func(x *Agent) bool { return x.ID == copilotJBID }) {
		t.Fatal("Copilot (JetBrains) is not among the agents")
	}
	a := copilotJetBrainsAt(path, func() bool { return false })
	f := a.Field("provider")
	if f == nil || f.Get() != "" || a.Wired() || a.Detected() {
		t.Fatalf("before: %v %v %v", f, a.Wired(), a.Detected())
	}
	if err := a.Apply("provider", magpieID); err != nil {
		t.Fatal(err)
	}
	if f.Get() != magpieID || !a.Wired() || !a.Detected() {
		t.Fatalf("after: %q %v %v", f.Get(), a.Wired(), a.Detected())
	}
	if d := a.Check(); d != "" {
		t.Fatalf("check: %s", d)
	}
	if st, _ := os.Stat(path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}

	file := map[string]json.RawMessage{}
	user := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(readFile(path)), &file); err != nil {
		t.Fatalf("%v\n%s", err, readFile(path))
	}
	json.Unmarshal([]byte(copilotJBUser), &user)
	for k, v := range user {
		if !sameJSON(string(file[k]), v) {
			t.Errorf("the user's %s changed: %s", k, file[k])
		}
	}

	var cfg struct {
		GroupName string `json:"groupName"`
		APIType   string `json:"apiType"`
	}
	strictJSON(t, file["magpie-provider-config"], &cfg)
	if cfg.GroupName != magpieID || cfg.APIType != "chatCompletions" {
		t.Errorf("provider config: %s", file["magpie-provider-config"])
	}
	var key string
	json.Unmarshal(file["magpie-api-key"], &key)
	if key != gateway.TokenFor(copilotJBID) {
		t.Errorf("key %q", key)
	}
	var models map[string]copilotJBModel
	strictJSON(t, file["magpie-models-config"], &models)
	m, ok := models["relay/glm-4.6"]
	if !ok {
		t.Fatalf("relay/glm-4.6 not listed: %s", file["magpie-models-config"])
	}
	c := m.ModelCapabilities
	if m.DeploymentURL != gatewayV1()+"/chat/completions" || m.IsRegistered == nil || !*m.IsRegistered ||
		m.IsCustomModel == nil || !*m.IsCustomModel || !c.ToolCalling || c.Vision == nil || c.Name == "" ||
		c.MaxInputTokens != 204800 || c.MaxOutputTokens <= 0 || c.MaxOutputTokens > c.MaxInputTokens/4 {
		t.Errorf("model: %+v", m)
	}

	// hidden in the IDE's Manage Models, it stays hidden through a sync
	var all map[string]map[string]any
	json.Unmarshal(file["magpie-models-config"], &all)
	all["relay/glm-4.6"]["isRegistered"] = false
	file["magpie-models-config"], _ = json.Marshal(all)
	b, _ := json.Marshal(file)
	os.WriteFile(path, append(b, '\n'), 0o600)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(readFile(path)), &file)
	models = nil
	strictJSON(t, file["magpie-models-config"], &models)
	if r := models["relay/glm-4.6"].IsRegistered; r == nil || *r {
		t.Errorf("a hidden model was shown again: %s", file["magpie-models-config"])
	}

	// the key changed elsewhere: told
	if err := os.WriteFile(path, bytes.Replace([]byte(readFile(path)), []byte(gateway.TokenFor(copilotJBID)), []byte("other"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if d := a.Check(); d == "" {
		t.Error("a changed key isn't told")
	}

	if err := a.Apply("provider", ""); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "" || a.Wired() {
		t.Fatalf("after off: %s", readFile(path))
	}
	file = map[string]json.RawMessage{}
	json.Unmarshal([]byte(readFile(path)), &file)
	if len(file) != len(user) {
		t.Errorf("after off: %s", readFile(path))
	}
	for k, v := range user {
		if !sameJSON(string(file[k]), v) {
			t.Errorf("the user's %s changed: %s", k, file[k])
		}
	}
	// off, a sync leaves it off
	if err := a.Sync(); err != nil || a.Wired() {
		t.Fatalf("sync after off: %v %s", err, readFile(path))
	}
}

// With no byok.json yet (Copilot never given a key), magpie's makes it as
// the server does: private, in a private folder.
func TestCopilotJetBrainsNewFile(t *testing.T) {
	home := syncHome(t)
	path := filepath.Join(home, ".config", "github-copilot", "byok.json")
	a := copilotJetBrainsAt(path, func() bool { return true })
	if err := a.Apply("provider", magpieID); err != nil {
		t.Fatal(err)
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(path)), &file); err != nil || len(file) != 3 {
		t.Fatalf("%v: %s", err, readFile(path))
	}
	if runtime.GOOS == "windows" {
		return
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(path)); st.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v", st.Mode().Perm())
	}
}

// byok.json is where the server keeps it: under an absolute
// XDG_CONFIG_HOME, else ~/.config/github-copilot, or
// %USERPROFILE%\AppData\Local\github-copilot on Windows.
func TestCopilotBYOKPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	if got, want := copilotBYOKPath(home), filepath.Join(home, "xdg", "github-copilot", "byok.json"); got != want {
		t.Errorf("XDG: %s, want %s", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative/xdg")
	want := filepath.Join(home, ".config", "github-copilot", "byok.json")
	if runtime.GOOS == "windows" {
		want = filepath.Join(home, "AppData", "Local", "github-copilot", "byok.json")
	}
	if got := copilotBYOKPath(home); got != want {
		t.Errorf("default: %s, want %s", got, want)
	}
}

// The agent is found by the plugin installed in a JetBrains IDE (or
// Android Studio), not by ~/.config/github-copilot, which every Copilot
// signed in on the machine has.
func TestCopilotJetBrainsDetected(t *testing.T) {
	home := syncHome(t)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("XDG_DATA_HOME", "")
	os.MkdirAll(filepath.Join(home, ".config", "github-copilot"), 0o700)
	os.WriteFile(filepath.Join(home, ".config", "github-copilot", "apps.json"), []byte("{}"), 0o600)
	if copilotJetBrains(home).Detected() {
		t.Fatal("found with no IDE")
	}
	var ide string
	switch runtime.GOOS {
	case "darwin":
		ide = filepath.Join(home, "Library", "Application Support", "JetBrains", "IntelliJIdea2026.1", "plugins")
	case "windows":
		ide = filepath.Join(home, "AppData", "Roaming", "JetBrains", "IntelliJIdea2026.1", "plugins")
	default:
		ide = filepath.Join(home, ".local", "share", "JetBrains", "IntelliJIdea2026.1")
	}
	os.MkdirAll(ide, 0o755)
	if copilotJetBrains(home).Detected() {
		t.Fatal("found with an IDE without the plugin")
	}
	os.MkdirAll(filepath.Join(ide, copilotJBPlugin), 0o755)
	if !copilotJetBrains(home).Detected() {
		t.Fatal("not found with the plugin installed")
	}
}
