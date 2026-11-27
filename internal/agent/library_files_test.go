package agent

import (
	"path/filepath"
	"strings"
	"testing"
)

// TomXPRIME (#7994, Windows, v0.1.4947): no Codex CLI, Codex app or Cindy
// here, only a ChatGPT sign-in added in magpie, which wrote ~/.codex/
// auth.json. Codex wasn't detected, but SharedHere read the folder with
// nothing left out, took it for one Codex's vendor app had made, and the
// Library listed Codex and linked skills into ~/.codex/skills. What the
// Library then wrote there kept it so: Codex's AGENTS.md, hooks.json,
// agents/ and a config.toml of MCP servers even made it detected, and on
// Windows, where cliHeld can't tell the CLI is gone, listed on the Agents
// page. Claude Code's ~/.claude, with the .credentials.json its sign-in
// writes off the Mac, is the same.
func TestSignInAndLibraryFilesAreNoAgent(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	type files = map[string]string
	cases := []struct {
		id, dir string
		make    func(home string) *Agent
		signIn  files
		synced  files
		own     []files // each alone, beside the rest, is the agent's
	}{
		{
			id: "codex", dir: ".codex", make: codex,
			signIn: files{"auth.json": `{"tokens":{}}`, "skills/my-skill/SKILL.md": "x"},
			synced: files{
				"AGENTS.md":            "Use tabs.\n",
				"RTK.md":               "rtk\n",
				"hooks.json":           `{"hooks":{}}`,
				"agents/reviewer.toml": "name = \"reviewer\"\n",
				"config.toml":          "[mcp_servers.fs]\ncommand = \"npx\"\n\n[shell_environment_policy]\nset = { A = \"1\" }\n",
			},
			own: []files{
				{"config.toml": "model = \"gpt-5.5\"\n\n[mcp_servers.fs]\ncommand = \"npx\"\n"},
				{"config.toml": ""},
				{"config.toml": "[mcp_servers\n"},
				{"sessions/2026/11/27/rollout.jsonl": "{}"},
				{"version.json": "{}"},
			},
		},
		{
			id: "claude", dir: ".claude", make: claude,
			signIn: files{".credentials.json": `{"claudeAiOauth":{}}`, "skills/my-skill/SKILL.md": "x"},
			synced: files{
				"CLAUDE.md":          "Use tabs.\n",
				"RTK.md":             "rtk\n",
				"agents/reviewer.md": "---\nname: reviewer\n---\n",
				"commands/review.md": "p\n",
				"rules/style.md":     "b\n",
				"settings.json":      `{"hooks":{"Stop":[]},"permissions":{"deny":["mcp__fs"]},"env":{"A":"1"},"enabledPlugins":{"x@m":true},"extraKnownMarketplaces":{}}`,
			},
			own: []files{
				{"settings.json": `{"model":"opus","env":{"A":"1"}}`},
				{"settings.json": `{}`},
				{"settings.json": `{"hooks":`},
				{"projects/-Users-me/session.jsonl": "{}"},
			},
		},
	}
	was := goneOS
	t.Cleanup(func() { goneOS = was })
	for _, sys := range []string{"windows", "darwin", "linux"} {
		goneOS = sys
		for _, tc := range cases {
			at := func(fs ...files) *Agent {
				t.Helper()
				home, _ := cindyDetectHome(t)
				for _, f := range fs {
					for p, s := range f {
						writeAt(t, filepath.Join(home, tc.dir, filepath.FromSlash(p)), s)
					}
				}
				return tc.make(home)
			}
			if a := at(tc.signIn); a.Detected() || a.SharedHere() {
				t.Errorf("%s %s: only the sign-in and skills: detected %v, SharedHere %v", sys, tc.id, a.Detected(), a.SharedHere())
			}
			if a := at(tc.signIn, tc.synced); a.Detected() || a.SharedHere() || a.CLIMissing() {
				t.Errorf("%s %s: the sign-in and what the Library writes: detected %v, SharedHere %v, CLI missing %v", sys, tc.id, a.Detected(), a.SharedHere(), a.CLIMissing())
			}
			for _, own := range tc.own {
				if a := at(tc.signIn, tc.synced, own); !a.Detected() {
					var names []string
					for p := range own {
						names = append(names, p)
					}
					t.Errorf("%s %s: %s beside the Library's files: not detected", sys, tc.id, strings.Join(names, ", "))
				}
			}
		}
	}
}

// Cursor CLI's ~/.cursor and Copilot CLI's ~/.copilot are their vendors'
// editors' too (dirShared): a folder those made is a target, one holding
// only what magpie's Library wrote there for it is not.
func TestLibraryFilesAreNoSharedFolder(t *testing.T) {
	cases := []struct {
		id, dir string
		make    func(home string) *Agent
		synced  map[string]string
		app     string // a file the vendor's app makes there
	}{
		{"cursor", ".cursor", cursor, map[string]string{
			"mcp.json": `{"mcpServers":{}}`, "hooks.json": `{"version":1}`, "agents/reviewer.md": "x", "skills/my-skill/SKILL.md": "x",
		}, "argv.json"},
		{"copilot", ".copilot", func(home string) *Agent { return copilot(home, filepath.Join(home, ".config")) }, map[string]string{
			"copilot-instructions.md": "Use tabs.\n", "mcp-config.json": `{"mcpServers":{}}`, "agents/reviewer.agent.md": "x",
			"hooks/rtk-rewrite.json": "{}", "skills/my-skill/SKILL.md": "x",
		}, "ide/lock.json"},
	}
	for _, tc := range cases {
		home, _ := cindyDetectHome(t)
		t.Setenv("COPILOT_HOME", "")
		for p, s := range tc.synced {
			writeAt(t, filepath.Join(home, tc.dir, filepath.FromSlash(p)), s)
		}
		if a := tc.make(home); a.Detected() || a.SharedHere() {
			t.Errorf("%s: only the Library's files: detected %v, SharedHere %v", tc.id, a.Detected(), a.SharedHere())
		}
		writeAt(t, filepath.Join(home, tc.dir, filepath.FromSlash(tc.app)), "{}")
		if a := tc.make(home); !a.SharedHere() {
			t.Errorf("%s: its vendor's app's %s beside them: not SharedHere", tc.id, tc.app)
		}
	}
}
