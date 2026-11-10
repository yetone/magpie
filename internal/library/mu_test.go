package library

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// mu is Pi 1.4.0 on its own agent folder (qybaihe/mu): its instructions,
// MCP servers (Pi's own mcp.json) and skills are there, and
// MU_AGENT_DIR moves all three. Pi's own folder is left alone.
func TestMuTarget(t *testing.T) {
	h := sandbox(t)
	d := filepath.Join(h, ".mu", "agent")
	write(t, filepath.Join(d, "settings.json"), "{}")
	// mu-agent's command, which names its mu.mjs (maildir-utils' mu is
	// no mu: agent.notMuAgent)
	testenv.Program(t, filepath.Join(h, "bin", "mu"), "#!/bin/sh\nexec node \"$HOME/lib/node_modules/mu-agent/kyrn/bin/mu.mjs\" \"$@\"\n")
	write(t, filepath.Join(h, "bin", "mu.cmd"), "node \"%dp0%\\node_modules\\mu-agent\\kyrn\\bin\\mu.mjs\" %*\r\n")
	tg := targetByID("mu")
	if tg == nil || tg.MCP == nil || tg.MCP.Path != filepath.Join(d, "mcp.json") || tg.MCP.Format != fmtPiNative ||
		tg.Instructions != filepath.Join(d, "AGENTS.md") || tg.Override != filepath.Join(d, "AGENTS.override.md") ||
		tg.Skills != filepath.Join(d, "skills") {
		t.Fatalf("%+v", tg)
	}
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "fs"}, Agents: []string{"mu"}}))
	if got := piServers(t, tg.MCP.Path); got["fs"]["command"] != "npx" {
		t.Fatalf("mcp.json: %s", read(t, tg.MCP.Path))
	}
	if exists(filepath.Join(h, ".pi", "agent", "mcp.json")) {
		t.Error("Pi's mcp.json written")
	}

	own := filepath.Join(h, "mu-elsewhere")
	t.Setenv("MU_AGENT_DIR", own)
	write(t, filepath.Join(own, "settings.json"), "{}")
	if tg := targetByID("mu"); tg == nil || tg.MCP.Path != filepath.Join(own, "mcp.json") || tg.Skills != filepath.Join(own, "skills") {
		t.Fatalf("MU_AGENT_DIR: %+v", tg)
	}
}
