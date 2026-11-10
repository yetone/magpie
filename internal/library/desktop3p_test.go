package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// desktopFiles are Claude Desktop's two files in the sandbox: the one it
// reads signed in with Anthropic, and Claude-3p's, read in its 3p mode.
func desktopFiles(t *testing.T, h string) (string, string) {
	t.Helper()
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	d, _ := os.UserConfigDir()
	p := filepath.Join(d, "Claude", "claude_desktop_config.json")
	p3 := filepath.Join(d, "Claude-3p", "claude_desktop_config.json")
	if runtime.GOOS == "windows" {
		p3 = filepath.Join(h, "AppData", "Local", "Claude-3p", "claude_desktop_config.json")
		// on Windows Desktop is here when its program is
		// (agent.DesktopApp), not its folders
		write(t, filepath.Join(h, "AppData", "Local", "AnthropicClaude", "claude.exe"), "")
	}
	if !strings.HasPrefix(p, h) {
		t.Skip("config dir outside the sandbox: " + p)
	}
	return p, p3
}

func desktopServers(t *testing.T, p string) map[string]map[string]any {
	t.Helper()
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if s := read(t, p); s != "" {
		if err := json.Unmarshal([]byte(s), &doc); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	return doc.MCPServers
}

// #297: Claude Desktop in its 3p mode (on magpie's gateway) reads its MCP
// servers from Claude-3p/claude_desktop_config.json, so a server the
// library gives it goes there as well as into Claude/'s, and goes from
// both; the user's own servers and keys in either stay.
func TestClaudeDesktop3pMCP(t *testing.T) {
	h := sandbox(t)
	p, p3 := desktopFiles(t, h)
	write(t, p, `{"deploymentMode": "3p", "mcpServers": {"mine": {"command": "x"}}}`)
	write(t, p3, `{"deploymentMode": "3p", "preferences": {"a": true}, "mcpServers": {"mine3p": {"command": "y"}}}`)

	tg := targetByID("claude-desktop")
	if tg == nil || tg.MCP == nil || tg.MCP.Path != p || !slices.Equal(tg.MCP.Also, []string{p3}) {
		t.Fatalf("claude-desktop target: %+v", tg.MCP)
	}
	// the user's own server in Claude-3p is one to bring in
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(v.FoundServers, func(f Found) bool { return f.Server.Name == "mine3p" }) {
		t.Errorf("mine3p not found: %+v", v.FoundServers)
	}

	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "fs"}, Agents: []string{"claude-desktop"}}))
	for _, f := range []string{p, p3} {
		if fs := desktopServers(t, f)["fs"]; fs["command"] != "npx" {
			t.Errorf("%s: fs is %v", f, fs)
		}
	}
	if s := desktopServers(t, p3); s["mine3p"] == nil || s["mine"] != nil {
		t.Errorf("Claude-3p's servers: %v", s)
	}
	if s := read(t, p3); !strings.Contains(s, `"deploymentMode": "3p"`) || !strings.Contains(s, `"preferences"`) {
		t.Errorf("Claude-3p's file: %s", s)
	}

	// a key the user added in Claude-3p's entry stays when it is written again
	var doc map[string]any
	json.Unmarshal([]byte(read(t, p3)), &doc)
	doc["mcpServers"].(map[string]any)["fs"].(map[string]any)["note"] = "mine"
	b, _ := json.Marshal(doc)
	write(t, p3, string(b))
	ok(t)(SaveServer("fs", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "fs2"}, Agents: []string{"claude-desktop"}}))
	if fs := desktopServers(t, p3)["fs"]; fs["note"] != "mine" || !slices.Equal(strList(fs["args"]), []string{"-y", "fs2"}) {
		t.Errorf("fs in Claude-3p: %v", fs)
	}

	// taken out of the library, it leaves neither file
	ok(t)(RemoveServer("fs"))
	for _, f := range []string{p, p3} {
		if s := desktopServers(t, f); s["fs"] != nil {
			t.Errorf("%s still has fs: %v", f, s)
		}
	}
	if desktopServers(t, p)["mine"] == nil || desktopServers(t, p3)["mine3p"] == nil {
		t.Error("the user's own servers went")
	}
}

// A server magpie gave Desktop before, only in Claude/'s file, reaches
// Claude-3p's on the next sync; one there only goes when it is removed.
func TestClaudeDesktop3pSync(t *testing.T) {
	h := sandbox(t)
	p, p3 := desktopFiles(t, h)
	write(t, p, `{}`)
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Agents: []string{"claude-desktop"}}))
	if _, err := os.Stat(filepath.Dir(p3)); err == nil {
		t.Fatal("Claude-3p made for a Desktop never in 3p mode")
	}

	// Desktop switched to 3p since (magpie's gateway made the folder)
	write(t, p3, `{"deploymentMode": "3p"}`)
	ok(t)(Sync())
	if fs := desktopServers(t, p3)["fs"]; fs["command"] != "npx" {
		t.Fatalf("fs not synced into Claude-3p: %s", read(t, p3))
	}

	// gone from Claude/'s file by hand: removing it still clears Claude-3p's
	write(t, p, `{}`)
	ok(t)(RemoveServer("fs"))
	if s := desktopServers(t, p3); s["fs"] != nil {
		t.Errorf("Claude-3p still has fs: %s", read(t, p3))
	}
}

// Desktop only ever run in 3p mode has no Claude folder: its servers go
// into Claude-3p's file alone, and no Claude folder is made.
func TestClaudeDesktop3pOnly(t *testing.T) {
	h := sandbox(t)
	p, p3 := desktopFiles(t, h)
	write(t, p3, `{"deploymentMode": "3p"}`)
	tg := targetByID("claude-desktop")
	if tg == nil || tg.MCP == nil || tg.MCP.Path != p3 || len(tg.MCP.Also) != 0 {
		t.Fatalf("claude-desktop target: %+v", tg)
	}
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Agents: []string{"claude-desktop"}}))
	if fs := desktopServers(t, p3)["fs"]; fs["command"] != "npx" {
		t.Errorf("Claude-3p: %s", read(t, p3))
	}
	if _, err := os.Stat(filepath.Dir(p)); err == nil {
		t.Error("Claude folder made")
	}
}
