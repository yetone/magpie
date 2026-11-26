package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Cindy's Pi, its default runtime, reads no mcp.json: each session runs
// with PI_CODING_AGENT_DIR at pi-agent-home/run-tmp/<random hex> and
// --no-extensions, its bundled Pi (0.84.4) is older than the first that
// reads mcp.json itself, and its servers come only from Cindy's own
// database (startSession in maker-core's agents/pi/index.ts, Cindy
// 0.1.148). v0.1.4777 (POM_JUMPING on X) wrote pi-agent-home/mcp.json all
// the same, read by nothing. A server given Cindy goes to its Codex alone,
// and what magpie wrote into Pi's file is taken out again, the user's own
// server kept, the file removed when nothing is left in it.
func TestCindyPiGetsNoMCPFile(t *testing.T) {
	h := sandbox(t)
	dir := cindyHere(t, h)
	pi := filepath.Join(dir, "pi-agent-home", "mcp.json")
	codex := filepath.Join(dir, "codex-home", "config.toml")

	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Agents: []string{"cindy"}}))
	ok(t)(SaveServer("", Server{Name: "web", Transport: "http", URL: "https://example.com/mcp", Agents: []string{"cindy"}}))
	if _, err := os.Stat(pi); !os.IsNotExist(err) {
		t.Fatalf("pi-agent-home/mcp.json was written (%v):\n%s", err, read(t, pi))
	}
	if got := read(t, codex); !strings.Contains(got, "[mcp_servers.fs]") || !strings.Contains(got, "[mcp_servers.web]") {
		t.Errorf("codex-home/config.toml:\n%s", got)
	}
	if l, err := load(); err != nil {
		t.Fatal(err)
	} else if a := l.applied("cindy"); !slices.Contains(a.MCP, "fs") || !slices.Contains(a.MCP, "web") {
		t.Errorf("applied for cindy: %v", a.MCP)
	}

	// what an earlier magpie wrote there, beside a server of the user's
	write(t, pi, `{"mcpServers": {"fs": {"command": "npx", "args": ["-y", "@mcp/fs"]}, "web": {"url": "https://example.com/mcp"}, "mine": {"command": "uvx", "args": ["mine"]}}}`)
	ok(t)(Sync())
	s := piServers(t, pi)
	if s["fs"] != nil || s["web"] != nil || s["mine"]["command"] != "uvx" {
		t.Fatalf("pi-agent-home/mcp.json after a sync:\n%s", read(t, pi))
	}

	// with magpie's alone in it, the file and the folder magpie made go;
	// a folder with Cindy's own files in it stays
	write(t, pi, `{"mcpServers": {"fs": {"command": "npx", "args": ["-y", "@mcp/fs"]}}}`)
	ok(t)(Sync())
	if _, err := os.Stat(filepath.Dir(pi)); !os.IsNotExist(err) {
		t.Errorf("pi-agent-home left (%v)", err)
	}
	keep := filepath.Join(dir, "pi-agent-home", "runtime", "state.json")
	write(t, keep, "{}")
	write(t, pi, `{"mcpServers": {"fs": {"command": "npx", "args": ["-y", "@mcp/fs"]}}}`)
	ok(t)(Sync())
	if _, err := os.Stat(pi); !os.IsNotExist(err) {
		t.Errorf("mcp.json left (%v):\n%s", err, read(t, pi))
	}
	if read(t, keep) != "{}" {
		t.Errorf("Cindy's own file in pi-agent-home lost")
	}
	if got := read(t, codex); !strings.Contains(got, "[mcp_servers.fs]") {
		t.Errorf("codex-home/config.toml lost fs:\n%s", got)
	}
}
