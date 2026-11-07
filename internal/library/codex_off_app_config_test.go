package library

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// what the Codex app (ChatGPT.app 26.1021) writes into config.toml for its
// browser, Chrome and computer use: plugins, marketplaces, the node_repl
// server with its env table and env_vars, a server it keeps off
const codexAppOwnConfig = `model = "gpt-5.5"

[marketplaces.openai-bundled]
source_type = "local"
source = "/Users/me/.codex/.tmp/bundled-marketplaces/openai-bundled"

[plugins."chrome@openai-bundled"]
enabled = true

[plugins."browser@openai-bundled"]
enabled = true

[features]
hooks = true

[mcp_servers.node_repl]
args = []
command = "/Applications/ChatGPT.app/Contents/Resources/cua_node/bin/node_repl"
env_vars = ["BROWSER_USE_ACCESS_EXECUTOR_ID", "BROWSER_USE_ACCESS_PIPE_PATH"]
startup_timeout_sec = 120

[mcp_servers.node_repl.env]
NODE_REPL_NODE_PATH = "/Applications/ChatGPT.app/Contents/Resources/cua_node/bin/node"
BROWSER_USE_AVAILABLE_BACKENDS = "chrome,iab,mcpapps"
NODE_REPL_TRUSTED_SERVICES = '{"browser":"/Users/me/.codex/plugins/cache/openai-bundled/browser/26.1021.31345/scripts/browser-service.mjs"}'

[mcp_servers.computer-use]
command = "./Codex Computer Use.app/Contents/SharedSupport/SkyComputerUseClient.app/Contents/MacOS/SkyComputerUseClient"
args = ["mcp"]
cwd = "."
enabled = false

[shell_environment_policy.set]
NODE_REPL_TRUSTED_BROWSER_CLIENT_SHA256S = "9230e2bd"

[desktop]
open_links_in = "app"
`

// darwish4021 on Discord: Codex switched off on magpie, its app's browser
// configuration was gone. The library gives Codex its servers one way on
// magpie and another off it (agent.Rewired); the app's own servers and
// everything else it wrote stay through both, imported into the library
// or not.
func TestCodexOffKeepsAppsOwnConfig(t *testing.T) {
	for _, imported := range []bool{false, true} {
		t.Run(map[bool]string{false: "own", true: "imported"}[imported], func(t *testing.T) {
			h := sandbox(t)
			resetRuns(t)
			runOn = true
			wired := true
			was := runWired
			runWired = func(a *agent.Agent) bool { return wired }
			t.Cleanup(func() { runOn, runWired = false, was })
			path := filepath.Join(h, ".codex", "config.toml")
			write(t, path, codexAppOwnConfig)
			if _, err := SaveServer("", Server{Name: "mem", Transport: "stdio", Command: "node", Args: []string{"server.js"}, Agents: []string{"codex"}}); err != nil {
				t.Fatal(err)
			}
			if imported {
				if _, err := ImportServer("node_repl"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Sync(); err != nil {
				t.Fatal(err)
			}
			on := read(t, path)
			if !strings.Contains(on, RunPath+"mem") {
				t.Fatalf("on magpie, not given through the gateway as it should be:\n%s", on)
			}
			wired = false
			if _, err := Sync(); err != nil {
				t.Fatal(err)
			}
			off := read(t, path)
			for _, l := range strings.Split(codexAppOwnConfig, "\n") {
				if l = strings.TrimSpace(l); l != "" && !strings.Contains(off, l) {
					t.Errorf("lost %q", l)
				}
			}
			if t.Failed() {
				t.Logf("on magpie:\n%s\n\noff:\n%s", on, off)
			}
		})
	}
}
