package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// jetBrainsConfigs is where a JetBrains IDE keeps its config folders on
// this system, under a sandbox home.
func jetBrainsConfigs(home string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "JetBrains")
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "JetBrains")
	}
	return filepath.Join(home, ".config", "JetBrains")
}

// Air in an IDE (the Air plugin, Settings › AI › Air) reads its custom ACP
// agents from ~/.jetbrains/acp.json, not the Air app's folder: magpie adds
// its Magpie agent there, keeps what the IDE and the user put in the file
// (AI Assistant's agents, default_mcp_settings), and taking it off takes
// out that entry and its OpenCode config alone.
func TestAirIDEAddsMagpieAgent(t *testing.T) {
	home, appDir := airHome(t, "/opt/bin/opencode")
	acp := filepath.Join(home, ".jetbrains", "acp.json")
	// the file as AI Assistant's "Add Custom Agent" leaves it, with an
	// agent of the user's
	user := `{
  "default_mcp_settings": {
    "use_idea_mcp": true,
    "use_custom_mcp": true
  },
  "agent_servers": {
    "Goose": {"command": "/usr/local/bin/goose", "args": ["acp"], "use_idea_mcp": false}
  }
}
`
	writeFile(t, acp, user)
	a := airIDE(home, filepath.Join(home, ".config"))
	if a.Path != acp {
		t.Fatalf("path %s", a.Path)
	}
	if err := a.Apply("model", "magpie/fake/m1"); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(home, ".jetbrains", "magpie-opencode.json")
	for k, v := range map[string]string{
		"default_mcp_settings.use_idea_mcp":        "true",
		"agent_servers.Goose.command":              "/usr/local/bin/goose",
		"agent_servers.Goose.use_idea_mcp":         "false",
		"agent_servers.Magpie.command":             "/opt/bin/opencode",
		"agent_servers.Magpie.args.0":              "acp",
		"agent_servers.Magpie.env.OPENCODE_CONFIG": cfg,
	} {
		if got, _ := edit.GetJSON(acp, k); got != v {
			t.Fatalf("%s = %q, want %q\n%s", k, got, v, readFile(acp))
		}
	}
	if v, _ := edit.GetJSON(cfg, "provider.magpie.options.baseURL"); v != gatewayV1() {
		t.Fatalf("baseURL = %q\n%s", v, readFile(cfg))
	}
	if v := a.Field("model").Get(); v != "magpie/fake/m1" {
		t.Fatalf("model = %q", v)
	}
	if !a.Detected() {
		t.Fatal("not detected with magpie's agent in acp.json")
	}
	if n := a.Notice(); !strings.Contains(n, "JetBrains IDE") {
		t.Fatalf("notice: %q", n)
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift: %+v", d)
	}
	// the Air app's own acp.json isn't this one, and isn't written
	if _, err := os.Stat(filepath.Join(appDir, "acp.json")); !os.IsNotExist(err) {
		t.Fatalf("the Air app's acp.json was written: %v", err)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := edit.GetJSON(acp, "agent_servers.Magpie"); ok {
		t.Fatalf("Magpie still in acp.json: %s", readFile(acp))
	}
	for _, k := range []string{"agent_servers.Goose.command", "default_mcp_settings.use_custom_mcp"} {
		if _, ok := edit.GetJSON(acp, k); !ok {
			t.Fatalf("%s went: %s", k, readFile(acp))
		}
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatalf("magpie's OpenCode config left: %v", err)
	}
}

// The Air plugin is bundled from 2026.3 and installable from 2026.2, so an
// IDE config folder of 2026.2 or later shows the agent; an older IDE, the
// Air app's folder or ~/.jetbrains alone (AI Assistant's) don't.
func TestAirIDEDetectedByIDEVersion(t *testing.T) {
	home, _ := airHome(t, "/opt/bin/opencode")
	cfg := filepath.Join(home, ".config")
	base := jetBrainsConfigs(home)
	for _, d := range []string{"IntelliJIdea2026.1", "Air", "Toolbox", "PyCharm2025.3"} {
		os.MkdirAll(filepath.Join(base, d), 0o755)
	}
	os.MkdirAll(filepath.Join(home, ".jetbrains"), 0o755)
	if airIDE(home, cfg).Detected() {
		t.Fatal("detected without an IDE of 2026.2 or later")
	}
	for _, d := range []string{"IntelliJIdea2026.3", "GoLand2026.2", "PyCharm2027.1"} {
		p := filepath.Join(base, d)
		os.MkdirAll(p, 0o755)
		if !airIDE(home, cfg).Detected() {
			t.Fatalf("not detected with %s", d)
		}
		os.RemoveAll(p)
	}
}
