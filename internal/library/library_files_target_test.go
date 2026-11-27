package library

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// TomXPRIME (#7994, Windows, v0.1.4947): with no Codex CLI, Codex app or
// Cindy here, only the ~/.codex/auth.json a ChatGPT sign-in in magpie
// wrote, the Library listed Codex (instructions, skills, MCP) and linked
// skills into ~/.codex/skills, while the Agents page and `magpie ls` rightly
// left Codex out. Claude Code's ~/.claude with the .credentials.json its
// sign-in writes off the Mac was the same.
func TestSignInFolderIsNoLibraryTarget(t *testing.T) {
	for _, tc := range []struct{ id, dir, file string }{
		{"codex", ".codex", "auth.json"},
		{"claude", ".claude", ".credentials.json"},
	} {
		h := sandbox(t)
		os.Remove(filepath.Join(h, "bin", tc.id))
		os.Remove(filepath.Join(h, "bin", tc.id+".exe"))
		os.RemoveAll(filepath.Join(h, tc.dir))
		write(t, filepath.Join(h, tc.dir, tc.file), `{"tokens":{}}`)
		write(t, filepath.Join(h, tc.dir, "skills", "learned", "SKILL.md"), "x")
		if tg := targetByID(tc.id); tg != nil {
			t.Errorf("%s: a target for ~/%s holding only its sign-in and skills: %v", tc.id, tc.dir, ids(Targets()))
		}
		ok(t)(CreateSkill("my-skill", "x", []string{"claude", "codex", "opencode"}))
		if _, err := os.Lstat(filepath.Join(h, tc.dir, "skills", "my-skill")); err == nil {
			t.Errorf("%s: a skill linked into ~/%s/skills", tc.id, tc.dir)
		}
	}
}

// What the Library gives an agent in its folder while it is a target is
// no reason for it to stay one. Each agent whose folder its vendor's other
// app shares is made a target by that app (Cindy's copy for Codex and
// Claude Code, the Cursor editor, VS Code for Copilot), given every kind
// the Library has for it, and the app taken away: before #7994, what the
// Library had written kept Codex and Claude Code detected, and the
// folders a target, for ever.
func TestLibraryFilesKeepNoAgentATarget(t *testing.T) {
	vscodeUserDir := func(h string) string {
		switch runtime.GOOS {
		case "darwin":
			return filepath.Join(h, "Library", "Application Support", "Code", "User")
		case "windows":
			return filepath.Join(h, "AppData", "Roaming", "Code", "User")
		}
		return filepath.Join(h, ".config", "Code", "User")
	}
	for _, tc := range []struct {
		id, dir string
		bin     string
		app     func(t *testing.T, h string) (gone func())
	}{
		{"codex", ".codex", "codex", func(t *testing.T, h string) func() {
			d := cindyHere(t, h)
			write(t, filepath.Join(d, "codex-package", "0.130.0", "bin", "codex"), "")
			write(t, filepath.Join(d, "codex-package", "0.130.0", "bin", "codex.exe"), "")
			return func() { os.RemoveAll(filepath.Join(d, "codex-package")) }
		}},
		{"claude", ".claude", "claude", func(t *testing.T, h string) func() {
			d := cindyHere(t, h)
			write(t, filepath.Join(d, "claude-code", "2.1.321", "claude"), "")
			write(t, filepath.Join(d, "claude-code", "2.1.321", "claude.exe"), "")
			return func() { os.RemoveAll(filepath.Join(d, "claude-code")) }
		}},
		{"cursor", ".cursor", "cursor-agent", func(t *testing.T, h string) func() {
			write(t, filepath.Join(h, ".cursor", "argv.json"), "{}")
			return func() { os.Remove(filepath.Join(h, ".cursor", "argv.json")) }
		}},
		{"copilot", ".copilot", "copilot", func(t *testing.T, h string) func() {
			vscodeUser(t, h)
			return func() { os.RemoveAll(vscodeUserDir(h)) }
		}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			h := sandbox(t)
			os.Remove(filepath.Join(h, "bin", tc.bin))
			os.Remove(filepath.Join(h, "bin", tc.bin+".exe"))
			os.RemoveAll(filepath.Join(h, tc.dir))
			gone := tc.app(t, h)
			if targetByID(tc.id) == nil {
				t.Fatalf("no %s target with its vendor's app here: %v", tc.id, ids(Targets()))
			}
			who := []string{tc.id}
			text := "Use tabs."
			ok(t)(SaveInstructions(InstructionsChange{Shared: &text, Agents: who}))
			ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Agents: who}))
			ok(t)(CreateSkill("my-skill", "x", who))
			if slices.Contains(hookAgents, tc.id) {
				ok(t)(SaveHook("", Hook{Name: "bark", Event: "turn-end", Command: "bark", Agents: who}))
			}
			if slices.Contains(SubagentAgents, tc.id) {
				s := reviewer
				s.Agents = who
				ok(t)(SaveSubagent("", s))
			}
			// Codex reads custom prompts no more (codexNoCommands)
			if slices.Contains(CommandAgents, tc.id) && tc.id != "codex" {
				ok(t)(SaveCommand("", Command{Name: "review", Description: "d", Prompt: "p", Agents: who}))
			}
			if slices.Contains(RuleAgents, tc.id) {
				ok(t)(SaveRule("", Rule{Name: "style", Body: "b", Agents: who}))
			}
			if slices.Contains(envAgents, tc.id) {
				ok(t)(SaveEnv("", EnvVar{Name: "FOO", Value: "magpie", Agents: who}))
			}
			var wrote []string
			filepath.WalkDir(filepath.Join(h, tc.dir), func(p string, d fs.DirEntry, err error) error {
				if r, _ := filepath.Rel(h, p); err == nil && !d.IsDir() {
					wrote = append(wrote, filepath.ToSlash(r))
				}
				return nil
			})
			t.Logf("the Library wrote: %v", wrote)

			gone()
			a, err := agent.Find(tc.id)
			if err != nil {
				t.Fatal(err)
			}
			if a.Detected() || a.SharedHere() {
				t.Errorf("only what the Library wrote in ~/%s: detected %v, SharedHere %v", tc.dir, a.Detected(), a.SharedHere())
			}
			if tg := targetByID(tc.id); tg != nil {
				t.Errorf("still a target with its vendor's app gone: %v", ids(Targets()))
			}
			// what it was given stays, listed to be taken out (held),
			// and nothing new is added
			ok(t)(CreateSkill("later", "x", who))
			if _, err := os.Lstat(filepath.Join(h, tc.dir, "skills", "later")); err == nil {
				t.Error("a new skill linked in after the app was gone")
			}
			if _, err := os.Lstat(filepath.Join(h, tc.dir, "skills", "my-skill")); err != nil {
				t.Errorf("the skill it was given went: %v", err)
			}
			v, err := Read(nil)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(v.Agents, func(a AgentView) bool { return a.ID == tc.id && a.Gone && a.Skills != "" }) {
				t.Errorf("not listed as gone, to take out what it has: %+v", v.Agents)
			}
		})
	}
}
