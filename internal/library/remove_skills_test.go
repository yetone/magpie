package library

import (
	"os"
	"path/filepath"
	"testing"
)

// Every skill out of the library at once (#449): each as RemoveSkill takes
// it — out of every agent, its folder kept aside in one backup, a folder of
// the user's only unlinked — and one that can't be taken out said, the rest
// gone all the same.
func TestRemoveSkills(t *testing.T) {
	h := sandbox(t)
	src := filepath.Join(h, "src/skills")
	skill(t, filepath.Join(src, "pdf"), "pdf", "Read PDFs")
	skill(t, filepath.Join(src, "xlsx"), "xlsx", "Sheets")
	ok(t)(InstallSkills(src, []string{"pdf", "xlsx"}, []string{"claude", "codex"}))
	skill(t, filepath.Join(h, ".claude/skills/notes"), "notes", "Mine")
	ok(t)(ImportSkill("notes"))
	ok(t)(EverySkillAgents([]string{"claude", "codex"}, true))
	for _, d := range []string{".claude/skills/notes", ".codex/skills/notes", ".codex/skills/pdf"} {
		if _, err := os.Stat(filepath.Join(h, d, "SKILL.md")); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}

	if _, err := RemoveSkills(nil); err == nil {
		t.Error("no skills named was taken")
	}
	if _, err := RemoveSkills([]string{"nope"}); err == nil {
		t.Error("only a skill the library hasn't was taken")
	}
	res, err := RemoveSkills([]string{"pdf", "notes", "xlsx", "nope", "pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unremoved) != 1 || res.Unremoved[0].What != "skill:nope" {
		t.Errorf("unremoved: %+v", res.Unremoved)
	}
	if v, _ := Read(nil); len(v.Skills) != 0 {
		t.Errorf("left in the library: %+v", v.Skills)
	}
	for _, d := range []string{".claude/skills/pdf", ".codex/skills/pdf", ".claude/skills/xlsx", ".claude/skills/notes", ".codex/skills/notes"} {
		if _, err := os.Lstat(filepath.Join(h, d)); !os.IsNotExist(err) {
			t.Errorf("%s is still there", d)
		}
	}
	// the user's folders stay; the library's own is kept aside, in one backup
	for _, d := range []string{"pdf", "xlsx"} {
		if _, err := os.Stat(filepath.Join(src, d, "SKILL.md")); err != nil {
			t.Errorf("the user's %s went with it", d)
		}
	}
	kept, _ := filepath.Glob(filepath.Join(BackupDir(), "*", "skills", "*"))
	if len(kept) != 1 || filepath.Base(kept[0]) != "notes" {
		t.Errorf("kept aside: %v", kept)
	} else if _, err := os.Stat(filepath.Join(kept[0], "SKILL.md")); err != nil {
		t.Error("notes' folder wasn't kept whole")
	}
}
