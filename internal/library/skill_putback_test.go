package library

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// BeiWeiji on X: a skill brought into the library from "In your agents"
// had no way back, and removed from the library it was gone from the
// agents too, and from "In your agents". Removed now, it goes back to the
// agents as they had it: the folder moved in goes back where it was, with
// what was changed in the library, each agent's own link to it is made
// again and a copy of the very same files copied back. It is in "In your
// agents" again, and the library's folder isn't left with the backups.
func TestRemovedImportGoesBackToTheAgents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links need Developer Mode on Windows")
	}
	h := sandbox(t)
	claude := filepath.Join(h, ".claude/skills/notes")
	skill(t, claude, "notes", "Mine")
	codex := filepath.Join(h, ".codex/skills/notes")
	if err := os.MkdirAll(filepath.Dir(codex), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../.claude/skills/notes", codex); err != nil {
		t.Fatal(err)
	}
	gemini := filepath.Join(h, ".gemini/skills/notes")
	skill(t, gemini, "notes", "Mine") // a byte copy

	ok(t)(ImportSkill("notes"))
	v, _ := Read(nil)
	if len(v.Skills) != 1 || !slices.Equal(v.Skills[0].Back, []string{"claude", "codex", "gemini"}) {
		t.Fatalf("skills: %+v", v.Skills)
	}
	if !ours(claude, "notes") || !ours(gemini, "notes") {
		t.Fatal("the agents weren't given the library's")
	}
	// changed in the library while it was there: the change goes back too
	write(t, filepath.Join(skillDir("notes"), "more.md"), "added in the library\n")

	ok(t)(RemoveSkill("notes"))
	if fi, err := os.Lstat(claude); err != nil || linkEntry(fi) {
		t.Fatalf("claude's folder isn't back: %v", err)
	}
	if read(t, filepath.Join(claude, "more.md")) == "" || read(t, filepath.Join(claude, "scripts/run.sh")) == "" {
		t.Error("claude's folder came back without its files")
	}
	if to, err := os.Readlink(codex); err != nil || to != "../../.claude/skills/notes" {
		t.Errorf("codex's link is %q, %v", to, err)
	}
	if fi, err := os.Lstat(gemini); err != nil || linkEntry(fi) || ours(gemini, "notes") || read(t, filepath.Join(gemini, "SKILL.md")) == "" {
		t.Errorf("gemini's copy isn't back: %v", err)
	}
	if _, err := os.Lstat(skillDir("notes")); !os.IsNotExist(err) {
		t.Error("the library still has its folder")
	}
	if m, _ := filepath.Glob(filepath.Join(BackupDir(), "*/skills/notes")); len(m) != 0 {
		t.Errorf("its folder went to the backups: %v", m)
	}
	v, _ = Read(nil)
	if len(v.Skills) != 0 || len(v.FoundSkills) != 1 || v.FoundSkills[0].Name != "notes" ||
		!slices.Equal(v.FoundSkills[0].Agents, []string{"claude", "codex"}) || !slices.Equal(v.FoundSkills[0].Copies, []string{"gemini"}) {
		t.Fatalf("after: skills %+v, found %+v", v.Skills, v.FoundSkills)
	}
	// and it can be brought in again
	ok(t)(ImportSkill("notes"))
}

// One an agent only linked to, in a folder elsewhere: the library linked
// to it there, and removed, the agent's link is made again. The folder
// itself never moved.
func TestRemovedLinkedImportGoesBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links need Developer Mode on Windows")
	}
	h := sandbox(t)
	dev := filepath.Join(h, "src/dev")
	skill(t, dev, "dev", "Work in progress")
	entry := filepath.Join(h, ".claude/skills/dev")
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dev, entry); err != nil {
		t.Fatal(err)
	}
	ok(t)(ImportSkill("dev"))
	if !ours(entry, "dev") {
		t.Fatal("claude wasn't given the library's")
	}
	ok(t)(RemoveSkill("dev"))
	if to, err := os.Readlink(entry); err != nil || to != dev {
		t.Errorf("claude's link is %q, %v", to, err)
	}
	if read(t, filepath.Join(dev, "SKILL.md")) == "" {
		t.Error("the folder elsewhere is gone")
	}
	v, _ := Read(nil)
	if len(v.FoundSkills) != 1 || !slices.Equal(v.FoundSkills[0].Agents, []string{"claude"}) {
		t.Fatalf("found: %+v", v.FoundSkills)
	}
}

// Something of the user's put where the folder was, while the library had
// it: it stays as it is, and the skill is removed as one that came from
// anywhere else is, its folder kept with the backups.
func TestPutBackLeavesTheUsersNewFolder(t *testing.T) {
	h := sandbox(t)
	claude := filepath.Join(h, ".claude/skills/notes")
	skill(t, claude, "notes", "Mine")
	ok(t)(ImportSkill("notes"))
	if err := unlink(claude); err != nil {
		t.Fatal(err)
	}
	skill(t, claude, "notes", "Another")
	if _, err := RemoveSkill("notes"); err != nil {
		t.Fatal(err)
	}
	if m := readMetaOf(t, claude); m != "Another" {
		t.Errorf("the user's folder was touched: %q", m)
	}
	if m, _ := filepath.Glob(filepath.Join(BackupDir(), "*/skills/notes/SKILL.md")); len(m) != 1 {
		t.Errorf("the library's folder isn't with the backups: %v", m)
	}
}

func readMetaOf(t *testing.T, p string) string {
	t.Helper()
	m, _ := readMeta(p)
	return m.Description
}
