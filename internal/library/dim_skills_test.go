package library

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Dim reads skills from ~/.agents/skills and a project's .agents/skills
// (arcships/dim's skills catalog; yyhhyyyyyy on X). That folder is
// Goose's too, and Codex reads it: a skill given to Dim and Codex is linked there
// once, and taken from Dim alone Codex gets its own again.
func TestDimSkillsShared(t *testing.T) {
	h := sandbox(t)
	t.Setenv("DIMCODE_HOME", "")
	os.MkdirAll(filepath.Join(h, ".dimcode-alpha"), 0o755)
	holdCLI(t, h, "dima")

	tg := targetByID("dim")
	if tg == nil || tg.Skills != filepath.Join(h, ".agents", "skills") {
		t.Fatalf("dim: %+v", tg)
	}
	// the same folder as Goose's; Codex reads it besides its own
	if !slices.Contains(tg.SkillsAlso, "goose") {
		t.Errorf("dim shares ~/.agents/skills with goose: %v", tg.SkillsAlso)
	}
	if cx := targetByID("codex"); cx == nil || !slices.Contains(cx.SkillsAlso, "dim") {
		t.Errorf("codex reads dim's skills too: %+v", cx)
	}
	if tg.MCP != nil || tg.Instructions != "" {
		t.Errorf("dim is given skills only: %+v", tg)
	}

	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	shared := filepath.Join(h, ".agents/skills/pdf")
	cx := filepath.Join(h, ".codex/skills/pdf")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"codex", "dim"}))
	if !ours(shared, "pdf") {
		t.Error("~/.agents/skills/pdf isn't the library's")
	}
	if _, err := os.Lstat(cx); !os.IsNotExist(err) {
		t.Error("codex has a second link to it")
	}
	ok(t)(SkillAgents("pdf", []string{"codex"}))
	if !ours(cx, "pdf") {
		t.Error("codex hasn't it once dim is off")
	}
	if _, err := os.Lstat(shared); !os.IsNotExist(err) {
		t.Error("~/.agents/skills still has it")
	}

	// a project's: its .agents/skills
	if ProjectSkillsDir("dim") != ".agents/skills" {
		t.Errorf("dim reads a project's .agents/skills, not %q", ProjectSkillsDir("dim"))
	}
	proj := filepath.Join(t.TempDir(), "app")
	write(t, filepath.Join(proj, "README"), "app\n")
	ok(t)(AddProject(proj))
	ok(t)(ProjectSkill(proj, "pdf", []string{"dim"}))
	if !ours(filepath.Join(proj, ".agents/skills/pdf"), "pdf") {
		t.Error("the project's .agents/skills/pdf isn't the library's")
	}
}
