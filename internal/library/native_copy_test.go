package library

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// cuaMCP is the shape of unified-computer-use's .mcp.json (cua_repl): a
// shell that execs a node_repl inside ChatGPT.app, with the app's browser
// access pipe passed on.
const cuaMCP = `{"mcpServers": {"cua_repl": {"command": "/bin/sh",
  "args": ["-c", "exec \"$@\"", "cua-repl", "/Applications/ChatGPT.app/Contents/Resources/cua_node/bin/node_repl"],
  "env_vars": ["BROWSER_USE_ACCESS_PIPE_PATH"], "startup_timeout_sec": 120}}}`

// nativeCopyHome is a Codex with a marketplace of its own beside
// openai-bundled, and a Claude Code plugin, each declaring servers.
func nativeCopyHome(t *testing.T) (home, tools, claude string) {
	t.Helper()
	home, cache := nativeCodexHome(t)
	write(t, filepath.Join(home, ".codex", "config.toml"), `[marketplaces.openai-bundled]
source = "/x/.tmp/bundled-marketplaces/openai-bundled"

[marketplaces.fixmarket]
source = "/src/fixmarket"

[plugins."tools@fixmarket"]
enabled = true
`)
	tools = filepath.Join(cache, "fixmarket", "tools", "1.0.0")
	write(t, filepath.Join(tools, ".codex-plugin", "plugin.json"), `{"name": "tools", "version": "1.0.0", "mcpServers": "./.mcp.json"}`)
	write(t, filepath.Join(tools, ".mcp.json"), `{"mcpServers": {
  "local_tools": {"command": "node", "args": ["server.mjs", "--verbose", "missing.js"], "cwd": ".",
    "env": {"MODE": "x"}, "env_vars": ["TOKEN"], "startup_timeout_sec": 10.5, "tool_timeout_sec": 3600},
  "bin_tools": {"command": "bin/serve", "cwd": "sub"},
  "linear": {"type": "http", "url": "https://mcp.linear.app/mcp"}
}}`)
	write(t, filepath.Join(tools, "server.mjs"), "")
	cu := filepath.Join(cache, "fixmarket", "computer", "1.0.0")
	write(t, filepath.Join(cu, ".codex-plugin", "plugin.json"), `{"name": "computer", "version": "1.0.0", "mcpServers": "./.mcp.json"}`)
	write(t, filepath.Join(cu, ".mcp.json"), cuaMCP)

	cc := filepath.Join(home, ".claude")
	claude = filepath.Join(cc, "plugins", "cache", "ccmarket", "fixture", "0.1.0")
	write(t, filepath.Join(cc, "plugins", "installed_plugins.json"), `{"version": 2, "plugins": {
  "fixture@ccmarket": [{"scope": "user", "installPath": "`+filepath.ToSlash(claude)+`", "version": "0.1.0"}]}}`)
	write(t, filepath.Join(claude, ".claude-plugin", "plugin.json"), `{"name": "fixture", "version": "0.1.0"}`)
	write(t, filepath.Join(claude, ".mcp.json"), `{"mcpServers": {
  "serve": {"command": "${CLAUDE_PLUGIN_ROOT}/bin/serve", "args": ["--root", "${CLAUDE_PLUGIN_ROOT}"]},
  "keeps": {"command": "node", "args": ["${CLAUDE_PLUGIN_DATA}/x.js"]},
  "asks": {"command": "node", "args": ["${user_config.api_key}"]}
}}`)
	return home, tools, claude
}

func nativePart(t *testing.T, v []NativePlugin, agent, plugin, name string) NativePart {
	t.Helper()
	for _, x := range partsOf(nativeOf(t, v, agent, plugin), "mcp") {
		if x.Name == name {
			return x
		}
	}
	t.Fatalf("no server %s in %s's %s", name, agent, plugin)
	return NativePart{}
}

// A server a plugin declares is offered as a library server: started from
// anywhere (the plugin's folder written in where the agent would put it),
// with what the agent passes on as references, and for no agent yet. One
// that works only inside its agent says why instead (0xRichardH on X).
func TestNativeServerCopies(t *testing.T) {
	_, tools, claude := nativeCopyHome(t)
	v := ReadNative()

	lt := nativePart(t, v, "codex", "tools", "local_tools")
	s := lt.server
	if lt.NoCopy != "" || s == nil {
		t.Fatalf("local_tools: %q %+v", lt.NoCopy, s)
	}
	if s.Command != "node" || !slices.Equal(s.Args, []string{filepath.Join(tools, "server.mjs"), "--verbose", "missing.js"}) {
		t.Errorf("a program on the PATH stays, a file of the plugin is a path from anywhere: %q %q", s.Command, s.Args)
	}
	if s.Env["MODE"] != "x" || s.Env["TOKEN"] != "${TOKEN}" || len(s.Env) != 2 {
		t.Errorf("env, and what Codex passes on as a reference: %v", s.Env)
	}
	if s.StartupTimeout != 11 || s.CallTimeout != 3600 {
		t.Errorf("timeouts: %d %d", s.StartupTimeout, s.CallTimeout)
	}
	if !s.Local || s.Agents == nil || len(s.Agents) != 0 {
		t.Errorf("local %v, agents %v", s.Local, s.Agents)
	}
	if b := nativePart(t, v, "codex", "tools", "bin_tools"); b.server == nil || b.server.Command != filepath.Join(tools, "sub", "bin", "serve") {
		t.Errorf("a command with a folder, from a relative cwd: %+v", b.server)
	}
	if l := nativePart(t, v, "codex", "tools", "linear"); l.server == nil || l.server.URL != "https://mcp.linear.app/mcp" || l.server.Local {
		t.Errorf("linear: %q %+v", l.NoCopy, l.server)
	}

	sv := nativePart(t, v, "claude", "fixture@ccmarket", "serve")
	if sv.server == nil || sv.server.Command != claude+"/bin/serve" || !slices.Equal(sv.server.Args, []string{"--root", claude}) {
		t.Fatalf("CLAUDE_PLUGIN_ROOT is the plugin's folder: %q %+v", sv.NoCopy, sv.server)
	}
	if sv.server.Env["CLAUDE_PLUGIN_ROOT"] != claude {
		t.Errorf("and the server is given it, as Claude Code gives it: %v", sv.server.Env)
	}
	for _, n := range []string{"keeps", "asks"} {
		if x := nativePart(t, v, "claude", "fixture@ccmarket", n); x.NoCopy != "agent" || x.server != nil {
			t.Errorf("%s reads what Claude Code fills in: %q %+v", n, x.NoCopy, x.server)
		}
	}

	// codex_app is the Codex app's: its marketplace is written out of the
	// app's bundle, and every tools/list goes through the app's pipe
	if x := nativePart(t, v, "codex", "codex-app-tools", "codex_app"); x.NoCopy != "app" || x.server != nil {
		t.Errorf("codex_app: %q %+v", x.NoCopy, x.server)
	}
	// cua_repl runs inside ChatGPT.app, from any marketplace
	if x := nativePart(t, v, "codex", "computer", "cua_repl"); x.NoCopy != "app" || x.server != nil {
		t.Errorf("cua_repl: %q %+v", x.NoCopy, x.server)
	}
}

// A copy is a library server for no agent, made once; one that works only
// inside its agent is refused with why.
func TestCopyNativeServer(t *testing.T) {
	_, tools, _ := nativeCopyHome(t)
	dir := nativeOf(t, ReadNative(), "codex", "tools").Dir
	ok(t)(CopyNativeServer("codex", dir, "local_tools"))
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	s := l.server("local_tools")
	if s == nil || s.Command != "node" || s.Args[0] != filepath.Join(tools, "server.mjs") || len(s.Agents) != 0 {
		t.Fatalf("copied: %+v", s)
	}
	if _, err := CopyNativeServer("codex", dir, "local_tools"); err == nil || !strings.Contains(err.Error(), "already has") {
		t.Errorf("a second copy: %v", err)
	}
	app := nativeOf(t, ReadNative(), "codex", "codex-app-tools").Dir
	if _, err := CopyNativeServer("codex", app, "codex_app"); err == nil || err.Error() != NoCopyWhy("app", "Codex") {
		t.Errorf("codex_app: %v", err)
	}
	if _, err := CopyNativeServer("claude", dir, "local_tools"); err == nil {
		t.Error("another agent's plugin of the same folder")
	}
	if l, _ := load(); l.server("codex_app") != nil || len(l.MCP) != 1 {
		t.Errorf("library: %+v", l.MCP)
	}
}
