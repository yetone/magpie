package agent

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// VS Code's chat: switched on, magpie's group joins the user's in
// chatLanguageModels.json, its token in each model's Authorization header
// and no apiKey (VS Code reads that from its secret storage only), and a new
// chat starts on the model picked; switched off, both files are as they were
func TestVSCode(t *testing.T) {
	home := syncHome(t)
	dir := filepath.Join(home, "vscode", "User")
	a := vscodeAt(dir)
	settings := `{
	// my editor
	"editor.fontSize": 14,
	"chat.defaultModel": "gpt-5",
	"chat.agent.enabled": true,
}
`
	groups := `// models I added
[
	{
		"name": "Ollama",
		"vendor": "ollama", // local
		"url": "http://localhost:11434"
	},
]
`
	writeFile(t, a.Path, settings)
	lm := filepath.Join(dir, "chatLanguageModels.json")
	writeFile(t, lm, groups)
	f := a.Field("model")
	if f.Get() != "gpt-5" || !a.Detected() || a.Wired() {
		t.Fatalf("before: %q %v", f.Get(), a.Wired())
	}
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "magpie/relay/glm-4.6" || !a.Wired() || a.Check() != "" {
		t.Fatalf("connected: %q %v %q", f.Get(), a.Wired(), a.Check())
	}
	if v, _ := edit.GetJSON(a.Path, vscodeDefault); v != "relay/glm-4.6" {
		t.Fatalf("chat.defaultModel %q\n%s", v, readFile(a.Path))
	}
	for _, keep := range []string{"// my editor", `"editor.fontSize": 14`, `"chat.agent.enabled": true`} {
		if !strings.Contains(readFile(a.Path), keep) {
			t.Fatalf("settings lost %q:\n%s", keep, readFile(a.Path))
		}
	}
	var list []map[string]any
	if err := json.Unmarshal(jsonc.ToJSON([]byte(readFile(lm))), &list); err != nil || len(list) != 2 || list[0]["vendor"] != "ollama" {
		t.Fatalf("groups: %v %v\n%s", err, list, readFile(lm))
	}
	for _, keep := range []string{"// models I added", "// local"} {
		if !strings.Contains(readFile(lm), keep) {
			t.Fatalf("groups lost %q:\n%s", keep, readFile(lm))
		}
	}
	g, _ := edit.GetJSONItem(lm, vscodeGroup)
	for k, want := range map[string]string{
		"vendor": "customendpoint", "name": "magpie", "apiType": "chat-completions",
		"models.0.id": "relay/glm-4.6", "models.0.url": gatewayV1() + "/chat/completions",
		"models.0.toolCalling": "true", "models.0.contextWindow": "204800",
		"models.0.requestHeaders.Authorization": "Bearer magpie",
	} {
		if got := gjson.Get(g, k).String(); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if gjson.Get(g, "apiKey").Exists() || gjson.Get(g, "models.0.apiKey").Exists() || gjson.Get(g, "url").Exists() {
		t.Fatalf("a key or a discovery url: %s", g)
	}
	if n := gjson.Get(g, "models.0.maxOutputTokens").Int(); n <= 0 || n > 204800/4 {
		t.Fatalf("output %d", n)
	}

	// a model added to the catalog is listed once synced
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"glm-4.6", "other"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if g, _ := edit.GetJSONItem(lm, vscodeGroup); gjson.Get(g, "models.#").Int() != 2 {
		t.Fatalf("not synced: %s", g)
	}

	// the gateway's URL changed under it: Check says so
	b := readFile(lm)
	writeFile(t, lm, strings.ReplaceAll(b, gatewayV1(), "http://127.0.0.1:9/v1"))
	if c := a.Check(); !strings.Contains(c, "chatLanguageModels.json") {
		t.Fatalf("check: %q", c)
	}
	writeFile(t, lm, b)

	// one of VS Code's own picked: magpie's models stay in its list
	if err := a.Apply("model", "auto"); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "auto" || !a.Wired() {
		t.Fatalf("own: %q %v", f.Get(), a.Wired())
	}
	if err := a.Apply("model", "magpie/relay/other"); err != nil {
		t.Fatal(err)
	}
	// switched off: back to auto, the last of the user's own, group gone
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(a.Path, vscodeDefault); v != "auto" || a.Wired() {
		t.Fatalf("disconnected: %q %v", v, a.Wired())
	}
	if _, ok := edit.GetJSONItem(lm, vscodeGroup); ok {
		t.Fatalf("group left:\n%s", readFile(lm))
	}
}

// on and off again with nothing else changed: both files byte for byte
func TestVSCodeRoundTrip(t *testing.T) {
	home := syncHome(t)
	dir := filepath.Join(home, "vscode", "User")
	a := vscodeAt(dir)
	settings := "{\n\t// mine\n\t\"chat.defaultModel\": \"claude-sonnet-4.5\",\n\t\"files.autoSave\": \"afterDelay\"\n}\n"
	groups := "[\n\t{\n\t\t\"name\": \"Ollama\",\n\t\t\"vendor\": \"ollama\"\n\t}\n]\n"
	lm := filepath.Join(dir, "chatLanguageModels.json")
	writeFile(t, a.Path, settings)
	writeFile(t, lm, groups)
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if readFile(a.Path) != settings || readFile(lm) != groups {
		t.Fatalf("not as it was:\n%s\n%s", readFile(a.Path), readFile(lm))
	}

	// no files at all: made, and emptied of magpie again
	home = syncHome(t)
	dir = filepath.Join(home, "fresh", "User")
	a = vscodeAt(dir)
	lm = filepath.Join(dir, "chatLanguageModels.json")
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	if a.Field("model").Get() != "magpie/relay/glm-4.6" || a.Check() != "" {
		t.Fatalf("fresh: %q\n%s", a.Field("model").Get(), readFile(lm))
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if v, ok := edit.GetJSON(a.Path, vscodeDefault); ok || a.Wired() {
		t.Fatalf("fresh disconnected: %q\n%s", v, readFile(a.Path))
	}
}

// its requests come with GitHubCopilotChat/<version>
func TestVSCodeUA(t *testing.T) {
	if got := usage.AgentOf("GitHubCopilotChat/0.69.0"); got != "vscode" {
		t.Fatalf("%q", got)
	}
}
