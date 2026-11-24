package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// sunset on Discord: the Library's MCP servers never reached a Codex
// session in Cindy. Cindy runs its Codex with CODEX_HOME at codex-home in
// its data folder and copies only plugin entries there from ~/.codex, so
// its servers are written into that config.toml, beside Cindy's plugin
// entries and the user's own server, and only magpie's taken out again.
// ~/.codex's file isn't touched for it.
func TestCindyMCP(t *testing.T) {
	h := sandbox(t)
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(cfg, "CindyGlobal", "codex-home", "config.toml")
	user := "[plugins.\"browser@openai-bundled\"]\nenabled = true\n\n[mcp_servers.mine]\ncommand = \"uvx\"\nargs = [\"mine\"]\n"
	write(t, p, user)
	codex := filepath.Join(h, ".codex", "config.toml")
	write(t, codex, "model = \"gpt-5.5\"\n")

	tg := targetByID("cindy")
	if tg == nil || tg.MCP == nil || tg.MCP.Path != p || tg.MCP.Format != fmtCodex {
		t.Fatalf("cindy has no MCP target at %s: %+v", p, tg)
	}
	if id, err := Takes("cindy", "mcp"); id != "cindy" || err != nil {
		t.Errorf("Takes: %q %v", id, err)
	}

	agents := []string{"cindy"}
	stdio := Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Env: map[string]string{"K": "V"}, Agents: agents}
	web := Server{Name: "web", Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer x"}, Agents: agents}
	ok(t)(SaveServer("", stdio))
	ok(t)(SaveServer("", web))

	var doc struct {
		Plugins map[string]map[string]any `toml:"plugins"`
		Servers map[string]map[string]any `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal([]byte(read(t, p)), &doc); err != nil {
		t.Fatalf("%v:\n%s", err, read(t, p))
	}
	if fs := doc.Servers["fs"]; fs["command"] != "npx" || fs["url"] != nil {
		t.Errorf("fs: %v\n%s", fs, read(t, p))
	}
	if w := doc.Servers["web"]; w["url"] != "https://example.com/mcp" {
		t.Errorf("web: %v\n%s", w, read(t, p))
	} else if hd, _ := w["http_headers"].(map[string]any); hd["Authorization"] != "Bearer x" {
		t.Errorf("web headers: %v", w)
	}
	if doc.Servers["mine"]["command"] != "uvx" || doc.Plugins["browser@openai-bundled"]["enabled"] != true {
		t.Errorf("Cindy's plugin entry or the user's server lost:\n%s", read(t, p))
	}
	if read(t, codex) != "model = \"gpt-5.5\"\n" {
		t.Errorf("~/.codex/config.toml was written for Cindy:\n%s", read(t, codex))
	}

	ok(t)(RemoveServer("fs"))
	ok(t)(RemoveServer("web"))
	got := read(t, p)
	if strings.Contains(got, "mcp_servers.fs") || strings.Contains(got, "mcp_servers.web") ||
		!strings.Contains(got, "[mcp_servers.mine]") || !strings.Contains(got, "browser@openai-bundled") {
		t.Errorf("after removing:\n%s", got)
	}
}

// Cindy opened, but no Codex session run yet: its data folder is there and
// codex-home isn't. Its servers still go to the file Cindy's Codex will
// read, which a later session finds.
func TestCindyMCPBeforeCodexRan(t *testing.T) {
	h := sandbox(t)
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg, "Cindy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"@mcp/fs"}, Agents: []string{"cindy"}}))
	if got := read(t, filepath.Join(dir, "codex-home", "config.toml")); !strings.Contains(got, "[mcp_servers.fs]") {
		t.Errorf("codex-home/config.toml:\n%s", got)
	}
}
