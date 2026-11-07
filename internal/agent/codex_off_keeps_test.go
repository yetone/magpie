package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// a config.toml as a Codex user of months has one: comments, a profile,
// MCP servers with their env tables, projects Codex trusts, notify, tui,
// features, sandbox settings, skills and a provider of their own. Codex
// 0.164 loads it as it is (no top-level profile, which it refuses), and
// what the Disconnect leaves of it too.
const codexOffUserConfig = `# my codex
model = "gpt-5.5"
model_reasoning_effort = "high"
approval_policy = "on-request"
sandbox_mode = "workspace-write"
notify = ["bash", "-lc", "afplay /System/Library/Sounds/Glass.aiff"]
personality = "pragmatic"

[features]
web_search_request = true

[tui]
notifications = true

[sandbox_workspace_write]
network_access = true

[shell_environment_policy]
inherit = "core"
set = { PATH = "/usr/bin:/bin" }

[profiles.work]
model = "gpt-5.4"
approval_policy = "never"

[model_providers.myrelay]
name = "My relay"
base_url = "https://relay.example.com/v1"

[mcp_servers.github]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-github"]

[mcp_servers.github.env]
GITHUB_TOKEN = "ghp_x"

[mcp_servers.figma]
url = "https://mcp.figma.com/mcp"

[projects."/Users/me/work/api"]
trust_level = "trusted"

[projects."/Users/me/work/web"]
trust_level = "trusted"

[projects.'C:\Users\me\work']
trust_level = "untrusted"

[[skills.config]]
path = "/Users/me/.codex/skills/x"
enabled = false
`

func codexOffUserLines() []string {
	var out []string
	for _, l := range strings.Split(codexOffUserConfig, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "model ") && !strings.HasPrefix(l, "model_reasoning_effort") {
			out = append(out, l)
		}
	}
	return out
}

// darwish4021 on Discord: switching Codex off on the Agents page lost
// Codex's configuration. Switched on and off again, every line of the
// user's config.toml is there, an edit made while on magpie too, the file
// still parses, nothing of magpie's but its provider table (kept for the
// threads started on it) is left, and a chat is back on the provider it
// was started on. The relay cases were red: bringOver had moved the
// relay's threads onto magpie, so giveTables no longer counted the
// relay's table and forgot its base_url, leaving the user's provider
// pointed at magpie's gateway.
func TestCodexOffKeepsUsersConfig(t *testing.T) {
	for _, tc := range []struct{ name, auth, extra string }{
		{"chatgpt", `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, ""},
		{"api", `{"OPENAI_API_KEY":"sk-x"}`, ""},
		{"none", "", ""},
		{"relay", `{"OPENAI_API_KEY":"sk-x"}`, "model_provider = \"myrelay\"\n"},
		{"relay-chatgpt", `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "model_provider = \"myrelay\"\n"},
	} {
		for _, edited := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{true: "-edited"}[edited], func(t *testing.T) {
				home, read := codexHome(t, tc.auth, tc.extra+codexOffUserConfig)
				dir := filepath.Join(home, ".codex")
				own := "openai"
				if tc.extra != "" {
					own = "myrelay"
				}
				const thread = "019a0000-0000-7000-8000-00000000c0ff"
				codexThreadOn(t, dir, thread, own)
				cx := codex(home)
				if _, err := cx.ConnectHow(); err != nil {
					t.Fatal(err)
				}
				SyncCatalog()
				on := read()
				if edited {
					on += "\n[projects.\"/Users/me/new\"]\ntrust_level = \"trusted\"\n\n[mcp_servers.added]\ncommand = \"added\"\n"
					os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(on), 0o644)
				}
				if err := codex(home).Disconnect(); err != nil {
					t.Fatal(err)
				}
				off := read()
				// the user's chats are listed where Codex is again
				if p := codexRowOn(t, dir, thread); p != own {
					t.Errorf("thread left on %q, want %q", p, own)
				}
				want := codexOffUserLines()
				if edited {
					want = append(want, `[projects."/Users/me/new"]`, `[mcp_servers.added]`, `command = "added"`)
				}
				for _, l := range want {
					if !strings.Contains(off, l) {
						t.Errorf("lost %q", l)
					}
				}
				if tc.extra != "" && !strings.Contains(off, strings.TrimSpace(tc.extra)) {
					t.Errorf("provider not back")
				}
				if !strings.Contains(off, `model = "gpt-5.5"`) {
					t.Errorf("model not back")
				}
				var parsed map[string]any
				if err := toml.Unmarshal([]byte(off), &parsed); err != nil {
					t.Errorf("left unloadable: %v", err)
				}
				// the provider table stays for the threads started on it;
				// nothing else of magpie's does
				for k, v := range parsed {
					if s, ok := v.(string); ok && strings.Contains(s, "magpie") {
						t.Errorf("magpie's %s left: %q", k, s)
					}
				}
				if d := os.Getenv("CODEX_OFF_DUMP"); d != "" {
					os.MkdirAll(filepath.Join(d, t.Name()), 0o755)
					os.WriteFile(filepath.Join(d, t.Name(), "config.toml"), []byte(off), 0o644)
				}
				if t.Failed() {
					t.Logf("connected:\n%s\n\noff:\n%s", on, off)
				}
			})
		}
	}
}
