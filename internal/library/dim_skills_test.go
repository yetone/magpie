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
	if tg.MCP != nil {
		t.Errorf("dim is given no MCP servers: %+v", tg)
	}

	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	shared := filepath.Join(h, ".agents/skills/pdf")
	cx := filepath.Join(h, ".codex/skills/pdf")
	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"codex", "dim"}))
	if !ours(shared, "pdf") {
		t.Error("~/.agents/skills/pdf isn't the library's")
	}
	// codex's own link stays: ChatGPT's Work reads its folder alone
	if !ours(cx, "pdf") || realDir(cx) != realDir(shared) {
		t.Error("codex hasn't its own link to the library's")
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

// lml on Discord: Library › Instructions said Dim keeps no user-wide
// instructions file. Dim reads ~/AGENTS.md, "# User instructions
// (~/AGENTS.md)" (read_instructions in arcships/dim's prompt_sources.rs,
// in dimcode-alpha 0.1.0-alpha.29's binary): magpie's part goes in there
// beside the user's own lines, and comes out again with them kept. A
// project's AGENTS.md is the file Dim reads in a project.
func TestDimInstructionsInHomeAgentsMD(t *testing.T) {
	h := sandbox(t)
	t.Setenv("DIMCODE_HOME", "")
	os.MkdirAll(filepath.Join(h, ".dimcode-alpha"), 0o755)
	holdCLI(t, h, "dima")

	p := filepath.Join(h, "AGENTS.md")
	if tg := targetByID("dim"); tg == nil || tg.Instructions != p {
		t.Fatalf("dim: %+v", tg)
	}
	write(t, p, "Mine.\n")
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"dim"}}))
	if s := read(t, p); s != "Mine.\n\n"+blockBegin+"\nUse tabs.\n"+blockEnd+"\n" && s != "Mine.\n"+blockBegin+"\nUse tabs.\n"+blockEnd+"\n" {
		t.Fatalf("~/AGENTS.md:\n%q", s)
	}
	v, err := ReadInstructions()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(v.Agents, func(a AgentInstructions) bool { return a.Agent == "dim" && a.Path == p && a.On }) {
		t.Errorf("the page doesn't list Dim's file: %+v", v.Agents)
	}
	ok(t)(SaveInstructions(InstructionsChange{Agents: []string{}}))
	if s := read(t, p); s != "Mine.\n" {
		t.Errorf("taken back:\n%q", s)
	}
	if ProjectInstrFile("dim") != "AGENTS.md" {
		t.Errorf("dim reads a project's AGENTS.md, not %q", ProjectInstrFile("dim"))
	}
}
