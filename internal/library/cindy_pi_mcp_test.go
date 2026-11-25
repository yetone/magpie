package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// POM_JUMPING on X (v0.1.4777): a Library MCP server enabled for Cindy says
// "enabled" but never reaches the agent. Cindy's Codex reads codex-home/
// config.toml (TestCindyMCP), but its Pi, Cindy's default runtime, reads
// pi-agent-home/mcp.json, a different format magpie wasn't writing. Both
// runtimes get the servers now, the one applied list tracking them, and a
// server removed again leaves both files.
func TestCindyPiMCP(t *testing.T) {
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
	pi := filepath.Join(dir, "pi-agent-home", "mcp.json")
	codex := filepath.Join(dir, "codex-home", "config.toml")

	// the user's own server in Pi's file is kept
	write(t, pi, `{"mcpServers": {"mine": {"command": "uvx", "args": ["mine"]}}}`)

	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Env: map[string]string{"K": "V"}, Agents: []string{"cindy"}}))

	if s := piServers(t, pi); s["fs"]["command"] != "npx" || s["mine"]["command"] != "uvx" {
		t.Fatalf("pi-agent-home/mcp.json after enable:\n%s", read(t, pi))
	}
	// Codex still gets it, and the shared applied list records it
	if got := read(t, codex); !strings.Contains(got, "[mcp_servers.fs]") {
		t.Errorf("codex-home/config.toml:\n%s", got)
	}
	if l, err := load(); err != nil {
		t.Fatal(err)
	} else if a := l.applied("cindy"); !slices.Contains(a.MCP, "fs") {
		t.Errorf("applied has no fs for cindy: %v", a.MCP)
	}

	// removing it takes it out of Pi's file, the user's own left
	ok(t)(RemoveServer("fs"))
	if s := piServers(t, pi); s["fs"] != nil || s["mine"]["command"] != "uvx" {
		t.Fatalf("pi-agent-home/mcp.json after remove:\n%s", read(t, pi))
	}
}
