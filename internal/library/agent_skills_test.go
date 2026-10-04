package library

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Every agent on the Agents page that reads skills has a place for them in
// the library (Kimi Code had none, so a skill never reached it): each its
// own folder, but Kimi Code, whose own would hide Claude Code's from it,
// the shared ~/.agents/skills.
func TestAgentsSkillsFolders(t *testing.T) {
	h := sandbox(t)
	for _, f := range []string{".kimi/config.toml", ".commandcode/settings.json", ".factory/settings.json", ".cline/data/globalState.json",
		".hermes/config.yaml", ".grok/config.toml", ".qoder/settings.json", ".workbuddy/models.json", ".fx/settings.json", ".config/devin/config.json"} {
		write(t, filepath.Join(h, f), "")
	}
	want := map[string]string{
		"kimi":        ".agents/skills",
		"goose":       ".agents/skills",
		"commandcode": ".commandcode/skills",
		"droid":       ".factory/skills",
		"cline":       ".cline/skills",
		"hermes":      ".hermes/skills",
		"grok":        ".grok/skills",
		"qoder":       ".qoder/skills",
		"workbuddy":   ".workbuddy/skills",
		"fx":          ".fx/skills",
		"devin":       ".config/devin/skills",
	}
	for id, d := range want {
		tg := targetByID(id)
		if tg == nil || tg.Skills != filepath.Join(h, d) {
			t.Errorf("%s: %+v", id, tg)
		}
	}
	// what Kimi Code reads first of the shared ones, when it's there
	os.MkdirAll(filepath.Join(h, ".config/agents/skills"), 0o755)
	if tg := targetByID("kimi"); tg == nil || tg.Skills != filepath.Join(h, ".config/agents/skills") {
		t.Errorf("kimi with ~/.config/agents/skills: %+v", tg)
	}
	if ProjectSkillsDir("kimi") != ".agents/skills" {
		t.Error("kimi reads a project's .agents/skills")
	}
}

// A skill given to Kimi Code is one link in ~/.agents/skills, which Goose
// shares; Codex and Gemini CLI, which read that folder as well, get no
// second link in their own, and get one again once it's not there.
func TestSharedSkillsWrittenOnce(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".kimi/config.toml"), "")
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	shared := filepath.Join(h, ".agents/skills/pdf")
	cx, cl := filepath.Join(h, ".codex/skills/pdf"), filepath.Join(h, ".claude/skills/pdf")

	ok(t)(InstallSkills(src, []string{"pdf"}, []string{"claude", "codex", "kimi"}))
	if !ours(shared, "pdf") {
		t.Error("~/.agents/skills/pdf isn't the library's")
	}
	if !ours(cl, "pdf") {
		t.Error("claude hasn't it")
	}
	if _, err := os.Lstat(cx); !os.IsNotExist(err) {
		t.Error("codex has a second link to it")
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	also := map[string][]string{}
	for _, a := range v.Agents {
		also[a.ID] = a.SkillsAlso
	}
	for id, w := range map[string]string{"codex": "kimi", "gemini": "goose", "kimi": "goose", "goose": "kimi"} {
		if !slices.Contains(also[id], w) {
			t.Errorf("%s reads %s's skills too: %v", id, w, also[id])
		}
	}
	if slices.Contains(also["claude"], "kimi") {
		t.Errorf("claude reads no ~/.agents/skills: %v", also["claude"])
	}

	// Kimi Code (and Goose) off: Codex gets its own again, the shared goes
	ok(t)(SkillAgents("pdf", []string{"claude", "codex"}))
	if !ours(cx, "pdf") {
		t.Error("codex hasn't it")
	}
	if _, err := os.Lstat(shared); !os.IsNotExist(err) {
		t.Error("~/.agents/skills still has it")
	}
	// on again: codex's own goes, not the skill
	ok(t)(SkillAgents("pdf", []string{"claude", "codex", "goose"}))
	if _, err := os.Lstat(cx); !os.IsNotExist(err) || !ours(shared, "pdf") {
		t.Error("not once in ~/.agents/skills")
	}
	if _, err := os.Stat(filepath.Join(src, "pdf/SKILL.md")); err != nil {
		t.Error("the user's folder went")
	}
}

// A skill of the user's own in ~/.agents/skills brought into the library
// stays there as it is, and Kimi Code, whose folder it is, has it without
// a word of its "own skill by that name".
func TestImportSharedSkillForKimi(t *testing.T) {
	h := sandbox(t)
	os.RemoveAll(filepath.Join(h, ".config/goose"))
	write(t, filepath.Join(h, ".kimi/config.toml"), "")
	shared := filepath.Join(h, ".agents/skills/orca")
	skill(t, shared, "orca", "Orca")
	ok(t)(ImportSkill("orca"))
	if fi, err := os.Lstat(shared); err != nil || !fi.IsDir() {
		t.Errorf("the shared folder's skill: %v", err)
	}
	v, _ := Read(nil)
	if len(v.Skills) != 1 || !slices.Equal(v.Skills[0].Agents, []string{"kimi"}) || len(v.Skills[0].Problems) != 0 {
		t.Errorf("skills: %+v", v.Skills)
	}
	ok(t)(Sync())
}

// Droid and Grok Build read a user-wide AGENTS.md in their folders
// (~/.factory, $GROK_HOME else ~/.grok): the library's instructions reach
// them there, after what the user wrote, which stays.
func TestDroidGrokInstructions(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".factory/settings.json"), "")
	g := filepath.Join(h, "grok-home")
	t.Setenv("GROK_HOME", g)
	write(t, filepath.Join(g, "config.toml"), "")
	files := map[string]string{"droid": filepath.Join(h, ".factory/AGENTS.md"), "grok": filepath.Join(g, "AGENTS.md")}
	for id, f := range files {
		if tg := targetByID(id); tg == nil || tg.Instructions != f {
			t.Fatalf("%s: %+v", id, tg)
		}
		write(t, f, "# Mine\n")
	}
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"droid", "grok"}}))
	for id, f := range files {
		if s := read(t, f); s != "# Mine\n\n"+blockBegin+"\nUse tabs.\n"+blockEnd+"\n" {
			t.Errorf("%s's AGENTS.md:\n%s", id, s)
		}
	}
}
