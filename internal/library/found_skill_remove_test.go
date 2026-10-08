package library

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// #1303 (sxwedo): a skill an agent has of its own could only be brought
// into the library, then removed from it. Removed where it is, it leaves
// every agent that has it, its copies and its shared entry with it, each
// kept with the backups; a folder the agents only linked to, and another
// skill by that name, stay.
func TestRemoveFoundSkill(t *testing.T) {
	h := sandbox(t)
	claude := filepath.Join(h, ".claude/skills")
	skill(t, filepath.Join(claude, "notes"), "notes", "Mine")
	skill(t, filepath.Join(h, ".codex/skills/notes"), "notes", "Mine") // a byte copy
	gemini := filepath.Join(h, ".gemini/skills/notes")
	skill(t, gemini, "notes", "Another")
	// the shared folder is Goose's too: it goes with Goose's backups
	shared := filepath.Join(h, ".agents/skills/pdf")
	skill(t, shared, "pdf", "PDFs")
	if err := os.Symlink(shared, filepath.Join(claude, "pdf")); err != nil {
		t.Fatal(err)
	}
	dev := filepath.Join(h, "src/dev")
	skill(t, dev, "dev", "Work in progress")
	if err := os.Symlink(dev, filepath.Join(claude, "dev")); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"notes", "pdf", "dev"} {
		ok(t)(RemoveFoundSkill(name))
	}
	for _, p := range []string{filepath.Join(claude, "notes"), filepath.Join(h, ".codex/skills/notes"), filepath.Join(claude, "pdf"), shared, filepath.Join(claude, "dev")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s is still there", p)
		}
	}
	for _, g := range []string{"*/claude/skills/notes/SKILL.md", "*/codex/skills/notes/SKILL.md", "*/*/skills/pdf/SKILL.md"} {
		if m, _ := filepath.Glob(filepath.Join(BackupDir(), g)); len(m) != 1 {
			t.Errorf("%s isn't kept with the backups: %v", g, m)
		}
	}
	if read(t, filepath.Join(dev, "SKILL.md")) == "" {
		t.Error("the folder Claude Code linked to is gone")
	}
	if read(t, filepath.Join(gemini, "SKILL.md")) == "" {
		t.Error("gemini's other notes was touched")
	}
	v, _ := Read(nil)
	var names []string
	for _, f := range v.FoundSkills {
		names = append(names, f.Name+":"+f.Agents[0])
	}
	if !slices.Equal(names, []string{"notes:gemini"}) {
		t.Errorf("found after: %v", names)
	}
	if _, err := RemoveFoundSkill("nothing"); err == nil {
		t.Error("a skill no agent has was removed")
	}
	if len(v.Skills) != 0 {
		t.Errorf("the library got skills: %+v", v.Skills)
	}
}
