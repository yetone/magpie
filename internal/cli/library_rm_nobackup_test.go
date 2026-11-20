package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/library"
)

// @Eternalfate__ on X: magpie library skill rm <name> --no-backup deletes
// the library's own folder with no copy in the backups; without the flag a
// copy is kept as ever; --all --no-backup --yes the same for every skill.
func TestLibrarySkillRmNoBackup(t *testing.T) {
	h := t.TempDir()
	for k, v := range map[string]string{"HOME": h, "USERPROFILE": h, "XDG_CONFIG_HOME": filepath.Join(h, ".config"), "CLAUDE_CONFIG_DIR": "", "CODEX_HOME": "", "APPDATA": "", "LOCALAPPDATA": ""} {
		t.Setenv(k, v)
	}
	if err := os.MkdirAll(filepath.Join(h, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h, ".claude/settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b", "c", "d"} {
		if _, err := library.CreateSkill(n, "A skill", []string{"claude"}); err != nil {
			t.Fatal(err)
		}
	}
	kept := func(name string) int {
		m, _ := filepath.Glob(filepath.Join(library.BackupDir(), "*", "skills", name))
		return len(m)
	}
	if err := libraryCmd([]string{"library", "skill", "rm", "a", "--no-backup"}); err != nil {
		t.Fatal(err)
	}
	if err := libraryCmd([]string{"library", "skill", "rm", "b"}); err != nil {
		t.Fatal(err)
	}
	if kept("a") != 0 || kept("b") != 1 {
		t.Errorf("a kept %d (want 0), b kept %d (want 1)", kept("a"), kept("b"))
	}
	if err := libraryCmd([]string{"library", "skill", "rm", "c", "--nope"}); err == nil {
		t.Error("an unknown flag was taken")
	}
	if err := libraryCmd([]string{"library", "skill", "rm", "--all", "--no-backup", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := library.Read(nil); len(v.Skills) != 0 {
		t.Errorf("left: %d", len(v.Skills))
	}
	if kept("c") != 0 || kept("d") != 0 {
		t.Errorf("--all --no-backup kept c %d, d %d", kept("c"), kept("d"))
	}
}
