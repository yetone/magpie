package library

import (
	"os"
	"path/filepath"
	"testing"
)

// @Eternalfate__ on X: a skill removed from the library always left a copy
// in ~/.magpie/backups. Removed with keep false, the library's own folder
// is deleted and nothing goes to the backups; a skill linked from a folder
// of the user's (~/.agents/skills among them) only loses the link, and the
// folder it points to stays, files and all. Kept, as by default, the
// folder still goes to the backups.
func TestRemoveSkillWithoutBackup(t *testing.T) {
	h := sandbox(t)
	ok(t)(CreateSkill("pdf", "Read PDFs", []string{"claude"}))
	ok(t)(CreateSkill("xlsx", "Sheets", []string{"claude"}))
	shared := filepath.Join(h, ".agents/skills/mine")
	skill(t, shared, "mine", "The user's own")
	ok(t)(InstallSkills(shared, []string{""}, []string{"claude"}))
	// a link inside the library's own folder is taken away, not followed
	if err := os.Symlink(shared, filepath.Join(skillDir("pdf"), "ref")); err != nil {
		t.Fatal(err)
	}

	ok(t)(RemoveSkillsHow([]string{"pdf", "mine"}, false))
	for _, p := range []string{skillDir("pdf"), skillDir("mine"), filepath.Join(h, ".claude/skills/pdf"), filepath.Join(h, ".claude/skills/mine")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s is still there", p)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(BackupDir(), "*", "skills", "*")); len(m) != 0 {
		t.Errorf("kept with the backups, though not asked: %v", m)
	}
	if read(t, filepath.Join(shared, "SKILL.md")) == "" {
		t.Error("the user's folder in ~/.agents/skills went with it")
	}

	// the default still keeps a copy
	ok(t)(RemoveSkill("xlsx"))
	if m, _ := filepath.Glob(filepath.Join(BackupDir(), "*", "skills", "xlsx", "SKILL.md")); len(m) != 1 {
		t.Errorf("xlsx isn't kept with the backups: %v", m)
	}
}

// The same for a skill an agent has of its own: removed without a backup
// its folders are deleted; a link only unlinked, its folder kept; and an
// entry in ~/.agents/skills, which other tools read, still goes to the
// backups, never deleted.
func TestRemoveFoundSkillWithoutBackup(t *testing.T) {
	h := sandbox(t)
	claude := filepath.Join(h, ".claude/skills")
	skill(t, filepath.Join(claude, "notes"), "notes", "Mine")
	skill(t, filepath.Join(h, ".codex/skills/notes"), "notes", "Mine")
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

	ok(t)(RemoveFoundSkillsHow([]string{"notes", "pdf", "dev"}, false))
	for _, p := range []string{filepath.Join(claude, "notes"), filepath.Join(h, ".codex/skills/notes"), filepath.Join(claude, "pdf"), filepath.Join(claude, "dev")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s is still there", p)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(BackupDir(), "*", "*", "skills", "notes")); len(m) != 0 {
		t.Errorf("notes kept with the backups, though not asked: %v", m)
	}
	if m, _ := filepath.Glob(filepath.Join(BackupDir(), "*", "*", "skills", "pdf", "SKILL.md")); len(m) != 1 {
		t.Errorf("the shared entry wasn't kept with the backups: %v", m)
	}
	if read(t, filepath.Join(dev, "SKILL.md")) == "" {
		t.Error("the folder Claude Code linked to is gone")
	}
}
