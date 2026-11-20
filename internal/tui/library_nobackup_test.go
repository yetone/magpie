package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/library"
)

// @Eternalfate__ on X: removing a skill always left a copy in magpie's
// backups. On the library page d, then D in place of the second d, takes
// it out with its folder deleted, no copy kept; d twice keeps one, as
// ever. A skill linked from a folder of the user's isn't offered D: only
// its link goes, the folder stays.
func TestLibraryDeleteWithoutBackup(t *testing.T) {
	h := home(t)
	for _, n := range []string{"keep", "gone"} {
		if _, err := library.CreateSkill(n, "A skill", []string{"claude"}); err != nil {
			t.Fatal(err)
		}
	}
	old := filepath.Join(h, ".claude/skills/old")
	write(t, filepath.Join(old, "SKILL.md"), "---\nname: old\ndescription: Not wanted\n---\n")
	ext := filepath.Join(h, ".agents/skills/mine")
	write(t, filepath.Join(ext, "SKILL.md"), "---\nname: mine\ndescription: Mine\n---\n")
	if _, err := library.InstallSkills(ext, []string{""}, []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	m := press(t, model{w: 120, h: 40}, "6")
	row := func(kind, name string) int {
		t.Helper()
		i := slices.IndexFunc(m.lib, func(r libRow) bool { return r.kind == kind && r.name == name })
		if i < 0 {
			t.Fatalf("no %s %s: %+v", kind, name, m.lib)
		}
		return i
	}
	backups := func(name string) []string {
		m, _ := filepath.Glob(filepath.Join(library.BackupDir(), "*", "skills", name))
		n, _ := filepath.Glob(filepath.Join(library.BackupDir(), "*", "*", "skills", name))
		return append(m, n...)
	}

	m.lrow = row("skill", "gone")
	m = press(t, m, "d")
	if !strings.Contains(m.flash, "D deletes its folder") {
		t.Fatalf("d doesn't say D: %q", m.flash)
	}
	m = press(t, m, "D")
	if _, err := os.Lstat(filepath.Join(library.Dir(), "skills", "gone")); !os.IsNotExist(err) {
		t.Fatalf("gone is still in the library, flash %q", m.flash)
	}
	if b := backups("gone"); len(b) != 0 {
		t.Errorf("gone kept with the backups: %v", b)
	}

	m.lrow = row("found-skill", "old")
	m = press(t, m, "d", "D")
	if _, err := os.Lstat(old); !os.IsNotExist(err) {
		t.Fatalf("old is still there, flash %q", m.flash)
	}
	if b := backups("old"); len(b) != 0 {
		t.Errorf("old kept with the backups: %v", b)
	}

	// linked: no D, and D alone does nothing
	m.lrow = row("skill", "mine")
	m = press(t, m, "d")
	if strings.Contains(m.flash, "D deletes") {
		t.Errorf("a linked skill is offered D: %q", m.flash)
	}
	m = press(t, m, "D")
	row("skill", "mine")
	if _, err := os.Stat(filepath.Join(ext, "SKILL.md")); err != nil {
		t.Fatal("the user's folder went")
	}

	// d twice still keeps a copy
	m.lrow = row("skill", "keep")
	m = press(t, m, "d", "d")
	if b := backups("keep"); len(b) != 1 {
		t.Errorf("keep isn't with the backups: %v, flash %q", b, m.flash)
	}
}
