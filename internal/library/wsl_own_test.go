package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// An agent in a WSL distro that keeps everything in its own folder —
// Hermes Agent first (#1323) — is the library's to give to as on this
// machine: skills copied into its folder in the distro, MCP servers into
// its config there, and never a Windows program.
func TestWSLOwnFolderTargets(t *testing.T) {
	sandbox(t)
	root := t.TempDir()
	h := filepath.Join(root, "home", "me")
	write(t, filepath.Join(h, ".hermes", "config.yaml"), "model:\n  default: claude-sonnet-4\n")
	write(t, filepath.Join(h, ".omp", "agent", "config.yml"), "{}\n")
	write(t, filepath.Join(h, ".factory", "settings.json"), "{}\n")
	write(t, filepath.Join(h, ".config", "opencode", "opencode.json"), "{}\n")
	write(t, filepath.Join(h, ".claude", "settings.json"), "{}\n")
	t.Cleanup(agent.FakeWSL(map[string]string{
		"Ubuntu": "home:/home/me\ndir:.hermes\nbin:hermes\ndir:.omp\nbin:omp\ndir:.factory\nbin:droid\ndir:.config/opencode\nbin:opencode\ndir:.claude\n",
	}, map[string]string{"Ubuntu": root}))

	const hermes = "hermes@wsl:Ubuntu"
	want := map[string][2]string{ // MCP, skills
		hermes:                {".hermes/config.yaml", ".hermes/skills"},
		"omp@wsl:Ubuntu":      {".omp/agent/mcp.json", ".omp/agent/skills"},
		"droid@wsl:Ubuntu":    {".factory/mcp.json", ".factory/skills"},
		"opencode@wsl:Ubuntu": {".config/opencode/opencode.json", ".config/opencode/skills"},
	}
	for id, w := range want {
		tg := targetByID(id)
		if tg == nil {
			t.Errorf("%s isn't a target: %v", id, ids(Targets()))
			continue
		}
		if tg.MCP == nil || tg.MCP.Path != filepath.Join(h, w[0]) || !tg.MCP.WSL || tg.MCP.Distro != "Ubuntu" || tg.Skills != filepath.Join(h, w[1]) || !tg.Copy || tg.Agent.ID != id {
			t.Errorf("%s: %+v (mcp %+v)", id, tg, tg.MCP)
		}
	}
	// OpenCode there reads the distro's Claude Code skills, not this machine's
	if tg := targetByID("opencode@wsl:Ubuntu"); tg != nil && (len(tg.SkillsAlso) != 1 || tg.SkillsAlso[0] != "claude@wsl:Ubuntu") {
		t.Errorf("opencode@wsl reads skills of %v", tg.SkillsAlso)
	}

	src := filepath.Join(home(), "src", "skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{hermes}))
	p := filepath.Join(h, ".hermes", "skills", "pdf")
	if fi, err := os.Lstat(p); err != nil || !fi.IsDir() || !ours(p, "pdf") {
		t.Fatalf("hermes@wsl has no copy of the skill: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home(), ".hermes", "skills", "pdf")); !os.IsNotExist(err) {
		t.Errorf("this machine's Hermes got it: %v", err)
	}

	ok(t)(SaveServer("", Server{Name: "fetch", Transport: "stdio", Command: "uvx", Args: []string{"mcp-server-fetch"}, Agents: []string{hermes}}))
	if s := read(t, filepath.Join(h, ".hermes", "config.yaml")); !strings.Contains(s, "fetch") || !strings.Contains(s, "uvx") || !strings.Contains(s, "claude-sonnet-4") {
		t.Errorf("hermes@wsl config.yaml:\n%s", s)
	}
	res, err := SaveServer("", Server{Name: "win", Transport: "stdio", Command: `C:\Program Files\nodejs\npx.cmd`, Args: []string{"x"}, Agents: []string{hermes}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) != 1 || res.Problems[0].Agent != hermes {
		t.Errorf("problems: %+v", res.Problems)
	}
	if strings.Contains(read(t, filepath.Join(h, ".hermes", "config.yaml")), "npx") {
		t.Error("the Windows program was given to hermes@wsl")
	}
}
