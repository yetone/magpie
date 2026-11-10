package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// craftUser is a config.json as Craft Agents saves one: a workspace, a
// Claude connection of the user's on its default, and settings of its own.
const craftUser = `{
  "workspaces": [{"id": "ws1", "name": "Mine", "rootPath": "/Users/me/craft", "createdAt": 1}],
  "activeWorkspaceId": "ws1",
  "activeSessionId": null,
  "llmConnections": [
    {"slug": "claude-max", "name": "Claude Max", "providerType": "anthropic", "authType": "oauth",
     "models": ["claude-opus-5-5", "claude-sonnet-5"], "defaultModel": "claude-opus-5-5", "createdAt": 1700000000000}
  ],
  "defaultLlmConnection": "claude-max",
  "colorTheme": "nord"
}
`

func craftHome(t *testing.T) (home, path string) {
	t.Helper()
	home, _ = museHome(t)
	t.Setenv("CRAFT_CONFIG_DIR", "")
	return home, filepath.Join(home, ".craft-agent", "config.json")
}

// craftFixture is a Craft Agents that has run under home: its config.json
// as craftUser.
func craftFixture(t *testing.T, home string) {
	t.Helper()
	t.Setenv("CRAFT_CONFIG_DIR", "")
	writeFile(t, filepath.Join(home, ".craft-agent", "config.json"), craftUser)
}

// craftRead is config.json, parsed.
func craftRead(t *testing.T, path string) map[string]any {
	t.Helper()
	var m map[string]any
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	return m
}

// craftOurs is magpie's connection in config.json, nil for none.
func craftOurs(m map[string]any) map[string]any {
	cs, _ := m["llmConnections"].([]any)
	for _, c := range cs {
		if c, _ := c.(map[string]any); c["slug"] == "magpie" {
			return c
		}
	}
	return nil
}

// TestCraftAgents: magpie adds a keyless pi_compat connection at the
// gateway's /agent/craft-agents/v1 and makes it the default, its model in
// its list, as Craft loads one (springg1998 on X); stepping out puts back
// what the user had.
func TestCraftAgents(t *testing.T) {
	home, path := craftHome(t)
	a := craftAgents(home)
	if a.Path != path {
		t.Fatalf("path: %s", a.Path)
	}
	f := a.Field("model")

	// no config.json: Craft hasn't run, and one magpie made it wouldn't load
	if err := f.Set("magpie/deepseek/pro"); err == nil || !strings.Contains(err.Error(), "open Craft Agents once") {
		t.Fatalf("no config: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("magpie made a config.json")
	}
	// one with no workspaces is no config Craft loads
	writeFile(t, path, `{"llmConnections":[]}`)
	if err := f.Set("magpie/deepseek/pro"); err == nil {
		t.Fatal("wrote a config with no workspaces")
	}

	writeFile(t, path, craftUser)
	var want map[string]any
	json.Unmarshal([]byte(craftUser), &want)
	if f.Get() != "claude-opus-5-5" || a.Check() != "" {
		t.Fatalf("own: %q %q", f.Get(), a.Check())
	}
	var vals []string
	for _, o := range f.Options(a.Values()) {
		vals = append(vals, o.Value)
	}
	if !strings.Contains(strings.Join(vals, " "), "claude-opus-5-5 magpie/deepseek/pro") {
		t.Fatalf("options: %v", vals)
	}

	for range 2 { // twice: one connection, and the user's default kept
		if err := f.Set("magpie/deepseek/pro"); err != nil {
			t.Fatal(err)
		}
	}
	m := craftRead(t, path)
	if m["defaultLlmConnection"] != "magpie" || m["colorTheme"] != "nord" || !reflect.DeepEqual(m["workspaces"], want["workspaces"]) {
		t.Fatalf("set: %v", m)
	}
	cs, _ := m["llmConnections"].([]any)
	if len(cs) != 2 || !reflect.DeepEqual(cs[0], want["llmConnections"].([]any)[0]) {
		t.Fatalf("connections: %v", cs)
	}
	o := craftOurs(m)
	base := gateway.AgentURL(gateway.URL(), "craft-agents") + "/v1"
	if o["providerType"] != "pi_compat" || o["authType"] != "none" || o["baseUrl"] != base ||
		o["defaultModel"] != "deepseek/pro" || o["name"] != "magpie" {
		t.Fatalf("magpie's: %v", o)
	}
	if ce, _ := o["customEndpoint"].(map[string]any); ce["api"] != "openai-completions" {
		t.Fatalf("customEndpoint: %v", o["customEndpoint"])
	}
	if _, ok := o["createdAt"].(float64); !ok {
		t.Fatalf("createdAt: %v", o["createdAt"])
	}
	// Craft resets a defaultModel its list hasn't to the first in it
	var ids []string
	for _, x := range o["models"].([]any) {
		ids = append(ids, x.(map[string]any)["id"].(string))
	}
	if !strings.Contains(" "+strings.Join(ids, " ")+" ", " deepseek/pro ") {
		t.Fatalf("models: %v", ids)
	}
	if f.Get() != "magpie/deepseek/pro" || a.Check() != "" {
		t.Fatalf("on: %q %q", f.Get(), a.Check())
	}

	// a name the user gave it in Craft stays; Sync writes nothing new
	m = craftRead(t, path)
	craftOurs(m)["name"] = "My magpie"
	renamed, _ := json.MarshalIndent(m, "", "  ")
	writeFile(t, path, string(renamed))
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if o := craftOurs(craftRead(t, path)); o["name"] != "My magpie" || o["defaultModel"] != "deepseek/flash" {
		t.Fatalf("renamed: %v", o)
	}
	before, _ := os.ReadFile(path)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatalf("Sync rewrote it:\n%s\n%s", before, after)
	}

	// magpie's connection pointed elsewhere
	b, _ := os.ReadFile(path)
	writeFile(t, path, strings.Replace(string(b), base, "http://elsewhere/v1", 1))
	if !strings.Contains(a.Check(), "http://elsewhere/v1") {
		t.Fatalf("check: %q", a.Check())
	}
	writeFile(t, path, string(b))

	// stepping out puts back what the user had
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if m := craftRead(t, path); !reflect.DeepEqual(m, want) {
		t.Fatalf("restore: %v", m)
	}
	if f.Get() != "claude-opus-5-5" {
		t.Fatalf("after: %q", f.Get())
	}

	// a user with no default of their own gets none back, and Craft picks
	// its first; one of their own models goes on their connection
	writeFile(t, path, strings.Replace(craftUser, `"defaultLlmConnection": "claude-max",`, "", 1))
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	m = craftRead(t, path)
	if _, has := m["defaultLlmConnection"]; has || craftOurs(m) != nil {
		t.Fatalf("no default of theirs: %v", m)
	}
	if f.Get() != "claude-sonnet-5" {
		t.Fatalf("own model: %q", f.Get())
	}
}

// TestCraftAgentsConfigDir: config.json is in $CRAFT_CONFIG_DIR where it is
// set (paths.ts).
func TestCraftAgentsConfigDir(t *testing.T) {
	home, _ := craftHome(t)
	dir := filepath.Join(home, "elsewhere")
	t.Setenv("CRAFT_CONFIG_DIR", dir)
	if a := craftAgents(home); a.Path != filepath.Join(dir, "config.json") {
		t.Fatalf("path: %s", a.Path)
	}
}
